package search

import (
	"context"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/store"
)

// Published is the scope for content on the target branch.
const Published = "published"

// Index writes documents to the search tables.
type Index struct{ DB *store.DB }

// Put upserts one document.
func (x *Index) Put(ctx context.Context, q store.Querier, repoID, scope, p, blobSHA string, d Doc) error {
	now := store.Millis(time.Now())
	heads := strings.Join(d.Headings, "\n")
	if _, err := store.Exec(ctx, q, `INSERT INTO search_docs (repo_id, scope, path, blob_sha, title, headings, body, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (repo_id, scope, path) DO UPDATE SET blob_sha = excluded.blob_sha, title = excluded.title, headings = excluded.headings, body = excluded.body, updated_at = excluded.updated_at`,
		repoID, scope, p, blobSHA, d.Title, heads, d.Body, now); err != nil {
		return err
	}
	if q.D() == store.SQLite {
		if _, err := store.Exec(ctx, q, `DELETE FROM search_fts WHERE repo_id = ? AND scope = ? AND path = ?`, repoID, scope, p); err != nil {
			return err
		}
		_, err := store.Exec(ctx, q, `INSERT INTO search_fts (repo_id, scope, path, title, headings, body) VALUES (?, ?, ?, ?, ?, ?)`, repoID, scope, p, d.Title, heads, d.Body)
		return err
	}
	return nil
}

// Delete removes a document.
func (x *Index) Delete(ctx context.Context, q store.Querier, repoID, scope, p string) error {
	if _, err := store.Exec(ctx, q, `DELETE FROM search_docs WHERE repo_id = ? AND scope = ? AND path = ?`, repoID, scope, p); err != nil {
		return err
	}
	if q.D() == store.SQLite {
		_, err := store.Exec(ctx, q, `DELETE FROM search_fts WHERE repo_id = ? AND scope = ? AND path = ?`, repoID, scope, p)
		return err
	}
	return nil
}

// Indexed returns path → blob sha for a scope (to index incrementally).
func (x *Index) Indexed(ctx context.Context, repoID, scope string) (map[string]string, error) {
	rows, err := store.Query(ctx, x.DB, `SELECT path, blob_sha FROM search_docs WHERE repo_id = ? AND scope = ?`, repoID, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var p, sha string
		if err := rows.Scan(&p, &sha); err != nil {
			return nil, err
		}
		out[p] = sha
	}
	return out, rows.Err()
}

// Hit is a search result.
type Hit struct {
	Path        string `json:"path"`
	Title       string `json:"title"`
	Snippet     string `json:"snippet"` // plain text; matched terms wrapped in \x02…\x03
	Heading     string `json:"heading,omitempty"`
	HeadingSlug string `json:"heading_slug,omitempty"`
}

// Terms splits a query into search words.
func Terms(q string) []string {
	f := strings.FieldsFunc(strings.ToLower(q), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if len(f) > 8 {
		f = f[:8]
	}
	return f
}

// Search finds documents matching every term (prefix match on the last one).
func (x *Index) Search(ctx context.Context, repoID, scope, query string, limit int) ([]Hit, error) {
	if len(Terms(query)) == 0 {
		return []Hit{}, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	repo, err := repos.Get(ctx, x.DB, repoID)
	if err != nil {
		return nil, err
	}
	current := repo.Scope()
	out := []Hit{}
	// A settings change takes effect before the queued rebuild completes.
	// Scan beyond hidden hits so they do not crowd out allowed results.
	const pageSize = 50
	for offset := 0; ; offset += pageSize {
		page, err := x.searchPage(ctx, repoID, scope, query, pageSize, offset)
		if err != nil {
			return nil, err
		}
		for _, hit := range page {
			if current.Contains(hit.Path) {
				out = append(out, hit)
				if len(out) == limit {
					return out, nil
				}
			}
		}
		if len(page) < pageSize {
			return out, nil
		}
	}
}

func (x *Index) searchPage(ctx context.Context, repoID, scope, query string, limit, offset int) ([]Hit, error) {
	terms := Terms(query)
	if len(terms) == 0 {
		return []Hit{}, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	var sqlq string
	var args []any
	if x.DB.Dialect == store.SQLite {
		parts := make([]string, len(terms))
		for i, t := range terms {
			parts[i] = `"` + t + `"*`
		}
		sqlq = `SELECT path, title, snippet(search_fts, 5, char(2), char(3), '…', 14), headings FROM search_fts
			WHERE search_fts MATCH ? AND repo_id = ? AND scope = ? ORDER BY bm25(search_fts, 0, 0, 0, 10.0, 4.0, 1.0), path LIMIT ? OFFSET ?`
		args = []any{strings.Join(parts, " "), repoID, scope, limit, offset}
	} else {
		parts := make([]string, len(terms))
		for i, t := range terms {
			parts[i] = t + ":*"
		}
		sqlq = `SELECT path, title, ts_headline('simple', body, to_tsquery('simple', ?), 'StartSel=' || chr(2) || ', StopSel=' || chr(3) || ', MaxWords=24, MinWords=8'), headings
			FROM search_docs WHERE repo_id = ? AND scope = ? AND tsv @@ to_tsquery('simple', ?) ORDER BY ts_rank(tsv, to_tsquery('simple', ?)) DESC, path LIMIT ? OFFSET ?`
		tq := strings.Join(parts, " & ")
		args = []any{tq, repoID, scope, tq, tq, limit, offset}
	}
	rows, err := store.Query(ctx, x.DB, sqlq, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Hit{}
	for rows.Next() {
		var h Hit
		var heads string
		if err := rows.Scan(&h.Path, &h.Title, &h.Snippet, &heads); err != nil {
			return nil, err
		}
		if h.Title == "" {
			h.Title = strings.TrimSuffix(path.Base(h.Path), path.Ext(h.Path))
		}
		for _, hd := range strings.Split(heads, "\n") {
			lh := strings.ToLower(hd)
			match := true
			for _, t := range terms {
				if !strings.Contains(lh, t) {
					match = false
					break
				}
			}
			if match && hd != "" && hd != h.Title {
				h.Heading, h.HeadingSlug = hd, Slug(hd)
				break
			}
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
