package consistency

import (
	"context"
	"strings"
	"time"

	"github.com/kmdn-app/kmdn/internal/llm"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/store"
)

// IndexPublished brings the repo's passages in line with its head: changed
// pages are re-chunked, removed pages dropped. It returns how many pages changed.
func (s *Service) IndexPublished(ctx context.Context, r repos.Repo) (int, error) {
	if r.HeadSHA == "" {
		return 0, nil
	}
	m := s.Repos.Mirror(r)
	sc := r.Scope()
	entries, err := m.Tree(ctx, r.HeadSHA, sc.Root)
	if err != nil {
		return 0, err
	}
	have := map[string]string{}
	rows, err := store.Query(ctx, s.DB, `SELECT path, blob_sha FROM passage_files WHERE repo_id = ?`, r.ID)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var p, sha string
		if err := rows.Scan(&p, &sha); err != nil {
			rows.Close()
			return 0, err
		}
		have[p] = sha
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	want := map[string]bool{}
	bySHA := map[string][]string{}
	var fetch []string
	for _, e := range entries {
		if e.Type != "blob" || !repos.IsMarkdown(e.Path) || !sc.Contains(e.Path) || e.Size > 1<<20 {
			continue
		}
		want[e.Path] = true
		if have[e.Path] != e.SHA {
			if len(bySHA[e.SHA]) == 0 {
				fetch = append(fetch, e.SHA)
			}
			bySHA[e.SHA] = append(bySHA[e.SHA], e.Path)
		}
	}
	changed := 0
	if len(fetch) > 0 {
		cred, err := s.Repos.Adapters.ForRepo(ctx, r)
		if err != nil {
			return 0, err
		}
		c, err := cred.Credential(ctx, r.ForgeRepo())
		if err != nil {
			return 0, err
		}
		for start := 0; start < len(fetch); start += 200 {
			batch := fetch[start:min(start+200, len(fetch))]
			err := s.DB.InTx(ctx, func(tx *store.Tx) error {
				return m.ReadBlobs(ctx, c, batch, func(sha string, content []byte) error {
					for _, p := range bySHA[sha] {
						if err := putPage(ctx, tx, r.ID, p, sha, Chunk(p, string(content))); err != nil {
							return err
						}
						changed++
					}
					return nil
				})
			})
			if err != nil {
				return changed, err
			}
		}
	}
	for p := range have {
		if !want[p] {
			if err := s.DB.InTx(ctx, func(tx *store.Tx) error { return putPage(ctx, tx, r.ID, p, "", nil) }); err != nil {
				return changed, err
			}
			changed++
		}
	}
	return changed, nil
}

// putPage replaces a page's passages (sha "" removes the page).
func putPage(ctx context.Context, tx *store.Tx, repoID, p, sha string, ps []Passage) error {
	if _, err := store.Exec(ctx, tx, `DELETE FROM passages WHERE repo_id = ? AND path = ?`, repoID, p); err != nil {
		return err
	}
	if _, err := store.Exec(ctx, tx, `DELETE FROM passage_files WHERE repo_id = ? AND path = ?`, repoID, p); err != nil {
		return err
	}
	if sha == "" {
		return nil
	}
	for _, x := range ps {
		if _, err := store.Exec(ctx, tx, `INSERT INTO passages (repo_id, path, seq, slug, heading, line, text, context, hash) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			repoID, p, x.Seq, x.Slug, x.Heading, x.Line, x.Text, x.Context, x.Hash); err != nil {
			return err
		}
	}
	_, err := store.Exec(ctx, tx, `INSERT INTO passage_files (repo_id, path, blob_sha) VALUES (?, ?, ?)`, repoID, p, sha)
	return err
}

// published loads the repo's indexed passages, in path order.
func (s *Service) published(ctx context.Context, repoID string) ([]Passage, error) {
	rows, err := store.Query(ctx, s.DB, `SELECT path, seq, slug, heading, line, text, context, hash FROM passages WHERE repo_id = ? ORDER BY path, seq`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Passage
	for rows.Next() {
		var p Passage
		if err := rows.Scan(&p.Path, &p.Seq, &p.Slug, &p.Heading, &p.Line, &p.Text, &p.Context, &p.Hash); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// vectors returns unit vectors for passages by hash, embedding the ones not
// seen before with the configured model.
func (s *Service) vectors(ctx context.Context, repoID string, ps []Passage) (map[string][]float32, error) {
	model := s.LLM.EmbeddingModel(ctx)
	out := map[string][]float32{}
	var hashes []string
	embed := map[string]string{}
	for _, p := range ps {
		if _, ok := embed[p.Hash]; !ok {
			embed[p.Hash] = p.Embedded()
			hashes = append(hashes, p.Hash)
		}
	}
	for start := 0; start < len(hashes); start += 400 {
		batch := hashes[start:min(start+400, len(hashes))]
		args := []any{model}
		for _, h := range batch {
			args = append(args, h)
		}
		rows, err := store.Query(ctx, s.DB, `SELECT hash, vector FROM passage_embeddings WHERE model = ? AND hash IN (?`+strings.Repeat(", ?", len(batch)-1)+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var h string
			var b []byte
			if err := rows.Scan(&h, &b); err != nil {
				rows.Close()
				return nil, err
			}
			out[h] = decode(b)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	var missing []string
	var texts []string
	for _, h := range hashes {
		if _, ok := out[h]; !ok {
			missing = append(missing, h)
			texts = append(texts, embed[h])
		}
	}
	if len(missing) == 0 {
		return out, nil
	}
	vecs, err := s.LLM.Embed(ctx, llm.Run{RepoID: repoID}, texts)
	if err != nil {
		return nil, err
	}
	now := store.Millis(time.Now())
	err = s.DB.InTx(ctx, func(tx *store.Tx) error {
		for i, h := range missing {
			v := normalize(vecs[i])
			out[h] = v
			if _, err := store.Exec(ctx, tx, `INSERT INTO passage_embeddings (model, hash, dim, vector, created_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, model, h, len(v), encode(v), now); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}
