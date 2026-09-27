// Package links resolves page links, keeps the published link index, checks
// revisions for broken links and rewrites links when pages move.
// See docs/specs/04-doc-engine.md#link-index-and-graph.
package links

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/kmdn-app/kmdn/internal/docengine"
	"github.com/kmdn-app/kmdn/internal/gitmirror"
	"github.com/kmdn-app/kmdn/internal/jobs"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
)

// Kinds of links.
const (
	KindRelative = "relative" // docs/a.md → ../b.md
	KindRoute    = "route"    // /docs/b (site route, mapped by .kmdn.yml routes)
	KindAnchor   = "anchor"   // #section in the same page
	KindExternal = "external" // https://…, mailto:…
)

// Target is where a link points.
type Target struct {
	Kind   string `json:"kind"`
	Path   string `json:"to_path,omitempty"` // repo path as written (before trying extensions)
	Anchor string `json:"anchor,omitempty"`
}

var scheme = func(s string) bool {
	i := strings.IndexByte(s, ':')
	if i <= 0 {
		return false
	}
	return strings.IndexFunc(s[:i], func(c rune) bool {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '+' || c == '.' || c == '-'
		return !ok
	}) < 0
}

// Resolve interprets a link destination found in page from.
func Resolve(repo repos.Repo, from, dest string) Target {
	if strings.HasPrefix(dest, "//") || scheme(dest) {
		return Target{Kind: KindExternal}
	}
	p, anchor, _ := strings.Cut(dest, "#")
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	if u, err := url.PathUnescape(p); err == nil {
		p = u
	}
	if p == "" {
		return Target{Kind: KindAnchor, Path: from, Anchor: anchor}
	}
	if strings.HasPrefix(p, "/") {
		return Target{Kind: KindRoute, Path: routeToPath(repo, p), Anchor: anchor}
	}
	dir := path.Dir(from)
	trailing := strings.HasSuffix(p, "/")
	rp := strings.TrimPrefix(path.Clean("/"+path.Join(dir, p)), "/")
	if trailing {
		rp += "/"
	}
	return Target{Kind: KindRelative, Path: rp, Anchor: anchor}
}

// routeToPath maps a site route to a repo path with the longest matching
// .kmdn.yml route prefix ("/docs/" → "docs/"); unmapped routes are taken
// as repo-root paths.
func routeToPath(repo repos.Repo, route string) string {
	best, to := "", ""
	for prefix, dst := range repo.KmdnYML.Routes {
		if strings.HasPrefix(route, prefix) && len(prefix) > len(best) {
			best, to = prefix, dst
		}
	}
	rest := strings.TrimPrefix(route, best)
	if best == "" {
		rest = strings.TrimPrefix(route, "/")
	}
	p := strings.TrimPrefix(path.Clean("/"+path.Join(to, rest)), "/")
	if strings.HasSuffix(route, "/") && p != "" {
		p += "/"
	}
	return p
}

// candidates lists the files a link path may mean, in order.
func candidates(p string) []string {
	if strings.HasSuffix(p, "/") || p == "" {
		return []string{p + "index.md", p + "README.md", p + "_index.md"}
	}
	if path.Ext(p) != "" {
		return []string{p}
	}
	return []string{p, p + ".md", p + "/index.md", p + "/README.md", p + "/_index.md"}
}

// Tree answers whether a file exists and what anchors a page has.
type Tree interface {
	Exists(p string) bool
	Headings(ctx context.Context, p string) ([]string, error)
}

// Broken says why a resolved link is broken ("" when it isn't).
func Broken(ctx context.Context, t Tree, target Target) (reason, file string) {
	switch target.Kind {
	case KindExternal:
		return "", ""
	case KindAnchor:
		file = target.Path
	default:
		for _, c := range candidates(target.Path) {
			if t.Exists(c) {
				file = c
				break
			}
		}
		if file == "" {
			return "missing_page", ""
		}
	}
	if target.Anchor == "" || !repos.IsMarkdown(file) {
		return "", file
	}
	hs, err := t.Headings(ctx, file)
	if err != nil {
		return "", file // can't tell: don't flag
	}
	for _, h := range hs {
		if h == target.Anchor || h == strings.ToLower(target.Anchor) {
			return "", file
		}
	}
	return "missing_anchor", file
}

// Service keeps the published index and answers link questions.
type Service struct {
	DB        *store.DB
	Repos     *repos.Service
	Revisions *revisions.Service
	Engine    *docengine.Engine
	Jobs      *jobs.Queue
	Log       *slog.Logger
	// Duplicates returns pairs of pages with duplicate passages (the
	// consistency report), drawn as dotted edges. Optional.
	Duplicates func(ctx context.Context, repoID string) ([][2]string, error)
}

func (s *Service) revisions() *revisions.Service { return s.Revisions }

// JobIndex re-indexes a repo's published links.
const JobIndex = "links.index"

// Register wires the index job to repo head changes.
func (s *Service) Register() {
	s.Jobs.Register(JobIndex, func(ctx context.Context, j jobs.Job) (any, error) {
		var p struct {
			RepoID string `json:"repo_id"`
		}
		if err := j.Decode(&p); err != nil {
			return nil, jobs.Permanent(err)
		}
		n, err := s.Reindex(ctx, p.RepoID)
		return map[string]int{"changed": n}, err
	})
	s.Repos.OnHeadChanged = append(s.Repos.OnHeadChanged, func(ctx context.Context, r repos.Repo, _, _ string) error {
		_, err := s.Jobs.Enqueue(ctx, s.DB, JobIndex, map[string]string{"repo_id": r.ID}, jobs.EnqueueOptions{Key: JobIndex + ":" + r.ID})
		return err
	})
}

// Reindex extracts links from markdown files whose blobs changed.
func (s *Service) Reindex(ctx context.Context, repoID string) (int, error) {
	r, err := repos.Get(ctx, s.DB, repoID)
	if err != nil {
		return 0, jobs.Permanent(err)
	}
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
	rows, err := store.Query(ctx, s.DB, `SELECT path, blob_sha FROM link_sources WHERE repo_id = ?`, r.ID)
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
	a, err := s.Repos.Adapters.ForRepo(ctx, r)
	if err != nil {
		return 0, err
	}
	cred, err := a.Credential(ctx, r.ForgeRepo())
	if err != nil {
		return 0, err
	}
	changed := 0
	for start := 0; start < len(fetch); start += 100 {
		batch := fetch[start:min(start+100, len(fetch))]
		blobs := map[string]string{}
		if err := m.ReadBlobs(ctx, cred, batch, func(sha string, content []byte) error {
			blobs[sha] = string(content)
			return nil
		}); err != nil {
			return changed, err
		}
		type page struct {
			path, sha string
			links     []docengine.Link
			headings  []string
		}
		var pages []page
		for sha, content := range blobs {
			ls, hs, err := s.Engine.Links(ctx, content)
			if err != nil {
				s.Log.Warn("extract links", "err", err, "repo", r.ID)
				continue
			}
			for _, p := range bySHA[sha] {
				pages = append(pages, page{p, sha, ls, slugs(hs)})
			}
		}
		err := s.DB.InTx(ctx, func(tx *store.Tx) error {
			for _, pg := range pages {
				if err := s.put(ctx, tx, r, pg.path, pg.sha, pg.links, pg.headings); err != nil {
					return err
				}
				changed++
			}
			return nil
		})
		if err != nil {
			return changed, err
		}
	}
	for p := range have {
		if !want[p] {
			if _, err := store.Exec(ctx, s.DB, `DELETE FROM links WHERE repo_id = ? AND from_path = ?`, r.ID, p); err != nil {
				return changed, err
			}
			if _, err := store.Exec(ctx, s.DB, `DELETE FROM link_sources WHERE repo_id = ? AND path = ?`, r.ID, p); err != nil {
				return changed, err
			}
			changed++
		}
	}
	return changed, nil
}

func slugs(hs []docengine.Heading) []string {
	out := make([]string, len(hs))
	for i, h := range hs {
		out[i] = h.Slug
	}
	return out
}

func (s *Service) put(ctx context.Context, tx *store.Tx, r repos.Repo, p, sha string, ls []docengine.Link, headings []string) error {
	if _, err := store.Exec(ctx, tx, `DELETE FROM links WHERE repo_id = ? AND from_path = ?`, r.ID, p); err != nil {
		return err
	}
	for _, l := range ls {
		t := Resolve(r, p, l.URL)
		if _, err := store.Exec(ctx, tx, `INSERT INTO links (repo_id, from_path, url, to_path, anchor, kind, line) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			r.ID, p, l.URL, t.Path, t.Anchor, t.Kind, l.Line); err != nil {
			return err
		}
	}
	hj, _ := json.Marshal(headings)
	_, err := store.Exec(ctx, tx, `INSERT INTO link_sources (repo_id, path, blob_sha, headings) VALUES (?, ?, ?, ?)
		ON CONFLICT (repo_id, path) DO UPDATE SET blob_sha = excluded.blob_sha, headings = excluded.headings`, r.ID, p, sha, string(hj))
	return err
}

// Inbound is a published link pointing at a page.
type Inbound struct {
	From   string `json:"from_path"`
	URL    string `json:"url"`
	Anchor string `json:"anchor,omitempty"`
	Line   int    `json:"line"`
}

// inbound lists published links that resolve to page p (trying the same
// extension candidates as Broken).
func (s *Service) inbound(ctx context.Context, repoID, p string) ([]Inbound, error) {
	forms := []string{p}
	if strings.HasSuffix(p, ".md") {
		stem := strings.TrimSuffix(p, ".md")
		forms = append(forms, stem)
		for _, idx := range []string{"/index", "/README", "/_index"} {
			if strings.HasSuffix(stem, idx) {
				dir := strings.TrimSuffix(stem, idx)
				forms = append(forms, dir, dir+"/")
			}
		}
	}
	ph := strings.TrimSuffix(strings.Repeat("?, ", len(forms)), ", ")
	args := []any{repoID}
	for _, f := range forms {
		args = append(args, f)
	}
	rows, err := store.Query(ctx, s.DB, `SELECT from_path, url, anchor, line FROM links WHERE repo_id = ? AND kind <> 'external' AND to_path IN (`+ph+`) ORDER BY from_path, line`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Inbound{}
	for rows.Next() {
		var in Inbound
		if err := rows.Scan(&in.From, &in.URL, &in.Anchor, &in.Line); err != nil {
			return nil, err
		}
		if in.From != p {
			out = append(out, in)
		}
	}
	return out, rows.Err()
}

// publishedTree answers Tree questions for the published head.
type publishedTree struct {
	s     *Service
	repo  repos.Repo
	files map[string]bool
}

func (s *Service) published(ctx context.Context, repo repos.Repo, sha string) (*publishedTree, error) {
	nodes, err := s.Repos.TreeAt(ctx, repo, sha)
	if err != nil {
		return nil, err
	}
	t := &publishedTree{s: s, repo: repo, files: map[string]bool{}}
	for _, n := range nodes {
		if n.Type == "file" {
			t.files[n.Path] = true
		}
	}
	return t, nil
}

func (t *publishedTree) Exists(p string) bool { return t.files[p] }

func (t *publishedTree) Headings(ctx context.Context, p string) ([]string, error) {
	var hj string
	err := store.QueryRow(ctx, t.s.DB, `SELECT headings FROM link_sources WHERE repo_id = ? AND path = ?`, t.repo.ID, p).Scan(&hj)
	if err != nil {
		return nil, err
	}
	var hs []string
	err = json.Unmarshal([]byte(hj), &hs)
	return hs, err
}

// Outbound is a link from a page, resolved and checked.
type Outbound struct {
	URL    string `json:"url"`
	Kind   string `json:"kind"`
	Path   string `json:"to_path,omitempty"` // the file it resolves to, when found
	Anchor string `json:"anchor,omitempty"`
	Line   int    `json:"line"`
	Image  bool   `json:"image,omitempty"`
	Broken string `json:"broken,omitempty"` // missing_page | missing_anchor
}

// check resolves every link in markdown from page p against t.
func (s *Service) check(ctx context.Context, repo repos.Repo, t Tree, p, markdown string) ([]Outbound, error) {
	ls, _, err := s.Engine.Links(ctx, markdown)
	if err != nil {
		return nil, err
	}
	out := make([]Outbound, 0, len(ls))
	for _, l := range ls {
		tg := Resolve(repo, p, l.URL)
		o := Outbound{URL: l.URL, Kind: tg.Kind, Anchor: tg.Anchor, Line: l.Line, Image: l.Kind == "image"}
		reason, file := Broken(ctx, t, tg)
		o.Broken, o.Path = reason, file
		if o.Path == "" && tg.Kind != KindExternal {
			o.Path = tg.Path
		}
		out = append(out, o)
	}
	return out, nil
}

// PageLinks is what the Links panel shows for a page.
type PageLinks struct {
	Outgoing []Outbound `json:"outgoing"`
	Incoming []Inbound  `json:"incoming"`
}

// ForPublished returns a published page's links.
func (s *Service) ForPublished(ctx context.Context, repo repos.Repo, p string) (PageLinks, error) {
	f, err := s.Repos.ReadFile(ctx, repo, p, "")
	if err != nil {
		return PageLinks{}, err
	}
	t, err := s.published(ctx, repo, repo.HeadSHA)
	if err != nil {
		return PageLinks{}, err
	}
	out, err := s.check(ctx, repo, t, p, f.Content)
	if err != nil {
		return PageLinks{}, err
	}
	in, err := s.inbound(ctx, repo.ID, p)
	if err != nil {
		return PageLinks{}, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Broken != "" && out[j].Broken == "" })
	return PageLinks{Outgoing: out, Incoming: in}, nil
}

var errNotMarkdown = errors.New("links: not a markdown page")

func notFound(err error) bool {
	return errors.Is(err, store.ErrNotFound) || errors.Is(err, gitmirror.ErrNotFound) || errors.Is(err, repos.ErrOutOfScope) || errors.Is(err, errNotMarkdown)
}
