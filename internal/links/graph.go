package links

import (
	"context"
	"net/http"
	"path"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
)

// GraphNode is a page in the link graph.
type GraphNode struct {
	Path   string `json:"path"`
	Title  string `json:"title"`
	Folder string `json:"folder"` // top-level folder under the content root ("" at the root)
	In     int    `json:"in"`
	Out    int    `json:"out"`
	Orphan bool   `json:"orphan,omitempty"` // no inbound links
	// Changed: the revision adds or edits it (revision scope).
	Changed bool `json:"changed,omitempty"`
}

// GraphEdge is one or more links from a page to another.
type GraphEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Count int    `json:"count"`
	// Broken: the target doesn't exist (To is where the link points).
	Broken bool `json:"broken,omitempty"`
}

// Graph is a repository's pages and the links between them.
type Graph struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
	// Duplicates are pages that repeat each other (consistency report).
	Duplicates []GraphDuplicate `json:"duplicates"`
}

// GraphDuplicate is a pair of pages with a duplicate passage.
type GraphDuplicate struct {
	A string `json:"a"`
	B string `json:"b"`
}

// pageTitle humanizes a file name ("remote-work.md" → "Remote work").
func pageTitle(p string) string {
	base := strings.TrimSuffix(path.Base(p), path.Ext(p))
	if base == "index" || base == "README" || base == "_index" {
		if dir := path.Base(path.Dir(p)); dir != "." && dir != "/" {
			base = dir
		}
	}
	base = strings.NewReplacer("-", " ", "_", " ").Replace(base)
	if base == "" {
		return p
	}
	return strings.ToUpper(base[:1]) + base[1:]
}

// Graph builds the published graph, or with rev, the graph as the revision
// sees it: its pages' current links replace their published ones.
func (s *Service) Graph(ctx context.Context, repo repos.Repo, rev *revisions.Revision) (Graph, error) {
	sha := repo.HeadSHA
	if rev != nil {
		sha = rev.BaseSHA
	}
	var tree Tree
	pages := map[string]bool{}
	changed := map[string]bool{}
	var rt *revisionTree
	if rev != nil {
		t, err := s.revisionTree(ctx, repo, *rev)
		if err != nil {
			return Graph{}, err
		}
		rt, tree = t, t
		for p := range t.base.files {
			if repos.IsMarkdown(p) && !t.gone[p] {
				pages[p] = true
			}
		}
		for p := range t.files {
			if repos.IsMarkdown(p) {
				pages[p], changed[p] = true, true
			}
		}
	} else {
		t, err := s.published(ctx, repo, sha)
		if err != nil {
			return Graph{}, err
		}
		tree = t
		for p := range t.files {
			if repos.IsMarkdown(p) {
				pages[p] = true
			}
		}
	}
	// Published links, minus those from pages the revision changes.
	type key struct{ from, to string }
	edges := map[key]*GraphEdge{}
	add := func(from, to string) {
		if from == to {
			return
		}
		file := ""
		for _, c := range candidates(to) {
			if tree.Exists(c) {
				file = c
				break
			}
		}
		if file != "" && !repos.IsMarkdown(file) {
			return // an image or other file: not a page link
		}
		e := GraphEdge{From: from, To: file}
		if file == "" {
			if path.Ext(to) != "" && !repos.IsMarkdown(to) {
				return
			}
			e.To, e.Broken = to, true
		}
		k := key{e.From, e.To}
		if edges[k] == nil {
			edges[k] = &e
		}
		edges[k].Count++
	}
	rows, err := store.Query(ctx, s.DB, `SELECT from_path, to_path FROM links WHERE repo_id = ? AND kind IN ('relative', 'route') AND to_path <> ''`, repo.ID)
	if err != nil {
		return Graph{}, err
	}
	var pub [][2]string
	for rows.Next() {
		var f, t string
		if err := rows.Scan(&f, &t); err != nil {
			rows.Close()
			return Graph{}, err
		}
		pub = append(pub, [2]string{f, t})
	}
	rows.Close()
	for _, l := range pub {
		if pages[l[0]] && !changed[l[0]] {
			add(l[0], l[1])
		}
	}
	if rt != nil {
		for p, f := range rt.files {
			if !repos.IsMarkdown(p) {
				continue
			}
			ls, _, err := s.Engine.Links(ctx, f.ContentMD)
			if err != nil {
				continue
			}
			for _, l := range ls {
				if l.Kind == "image" {
					continue
				}
				tg := Resolve(repo, p, l.URL)
				if tg.Kind == KindRelative || tg.Kind == KindRoute {
					add(p, tg.Path)
				}
			}
		}
	}
	g := Graph{Nodes: []GraphNode{}, Edges: []GraphEdge{}, Duplicates: []GraphDuplicate{}}
	if s.Duplicates != nil {
		dups, err := s.Duplicates(ctx, repo.ID)
		if err != nil {
			return Graph{}, err
		}
		for _, d := range dups {
			if pages[d[0]] && pages[d[1]] {
				g.Duplicates = append(g.Duplicates, GraphDuplicate{A: d[0], B: d[1]})
			}
		}
	}
	in, out := map[string]int{}, map[string]int{}
	for _, e := range edges {
		g.Edges = append(g.Edges, *e)
		out[e.From]++
		if !e.Broken {
			in[e.To]++
		}
	}
	root := strings.Trim(repo.ContentRoot, "/")
	for p := range pages {
		rel := strings.TrimPrefix(strings.TrimPrefix(p, root), "/")
		folder := ""
		if i := strings.Index(rel, "/"); i > 0 {
			folder = rel[:i]
		}
		g.Nodes = append(g.Nodes, GraphNode{Path: p, Title: pageTitle(p), Folder: folder, In: in[p], Out: out[p], Orphan: in[p] == 0, Changed: changed[p]})
	}
	sort.Slice(g.Nodes, func(i, j int) bool { return g.Nodes[i].Path < g.Nodes[j].Path })
	sort.Slice(g.Edges, func(i, j int) bool {
		if g.Edges[i].From != g.Edges[j].From {
			return g.Edges[i].From < g.Edges[j].From
		}
		return g.Edges[i].To < g.Edges[j].To
	})
	return g, nil
}

// graph serves GET /repos/{repo}/graph?scope=published|revision:<id>.
func (s *Service) graph(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := s.role(w, r, chi.URLParam(r, "repo"))
	if !ok {
		return
	}
	var rev *revisions.Revision
	if scope := r.URL.Query().Get("scope"); strings.HasPrefix(scope, "revision:") {
		rv, err := revisions.Get(r.Context(), s.DB, strings.TrimPrefix(scope, "revision:"))
		if err != nil || rv.RepoID != repo.ID {
			api.Error(w, r, api.ErrNotFound)
			return
		}
		rev = &rv
	}
	g, err := s.Graph(r.Context(), repo, rev)
	if err != nil {
		fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, g)
}
