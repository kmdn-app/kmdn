package links

import (
	"context"
	"path"
	"sort"
	"strings"

	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
)

// Applier edits a page in a revision as a collaborative change (collab.Hub).
type Applier interface {
	Apply(ctx context.Context, repo repos.Repo, rev revisions.Revision, c revisions.Caller, p, markdown, kind string) error
}

// ApplierFunc adapts a function to Applier.
type ApplierFunc func(ctx context.Context, repo repos.Repo, rev revisions.Revision, c revisions.Caller, p, markdown, kind string) error

// Apply implements Applier.
func (f ApplierFunc) Apply(ctx context.Context, repo repos.Repo, rev revisions.Revision, c revisions.Caller, p, markdown, kind string) error {
	return f(ctx, repo, rev, c, p, markdown, kind)
}

// revisionTree is the base tree with the revision's manifest applied.
type revisionTree struct {
	s      *Service
	base   *publishedTree
	files  map[string]revisions.File // manifest by path (non-deleted)
	assets map[string]bool           // images uploaded into the revision
	gone   map[string]bool           // deleted or renamed away
	heads  map[string][]string
	repo   repos.Repo
	baseSH string
}

func (s *Service) revisionTree(ctx context.Context, repo repos.Repo, rev revisions.Revision) (*revisionTree, error) {
	base, err := s.published(ctx, repo, rev.BaseSHA)
	if err != nil {
		return nil, err
	}
	files, err := revisions.Files(ctx, s.DB, rev.ID)
	if err != nil {
		return nil, err
	}
	t := &revisionTree{s: s, base: base, files: map[string]revisions.File{}, assets: map[string]bool{}, gone: map[string]bool{}, heads: map[string][]string{}, repo: repo, baseSH: rev.BaseSHA}
	as, err := revisions.Assets(ctx, s.DB, rev.ID)
	if err != nil {
		return nil, err
	}
	for _, a := range as {
		t.assets[a.Path] = true
	}
	for _, f := range files {
		switch f.Op {
		case revisions.OpDelete:
			t.gone[f.Path] = true
		case revisions.OpRename:
			t.gone[f.FromPath] = true
			t.files[f.Path] = f
		default:
			t.files[f.Path] = f
		}
	}
	return t, nil
}

func (t *revisionTree) Exists(p string) bool {
	if _, ok := t.files[p]; ok || t.assets[p] {
		return true
	}
	return !t.gone[p] && t.base.Exists(p)
}

func (t *revisionTree) Headings(ctx context.Context, p string) ([]string, error) {
	if hs, ok := t.heads[p]; ok {
		return hs, nil
	}
	var hs []string
	if f, ok := t.files[p]; ok {
		_, h, err := t.s.Engine.Links(ctx, f.ContentMD)
		if err != nil {
			return nil, err
		}
		hs = slugs(h)
	} else if cached, err := t.base.Headings(ctx, p); err == nil {
		hs = cached
	} else {
		f, err := t.s.Repos.ReadFile(ctx, t.repo, p, t.baseSH)
		if err != nil {
			return nil, err
		}
		_, h, err := t.s.Engine.Links(ctx, f.Content)
		if err != nil {
			return nil, err
		}
		hs = slugs(h)
	}
	t.heads[p] = hs
	return hs, nil
}

// content returns a page's markdown as the revision sees it.
func (t *revisionTree) content(ctx context.Context, p string) (string, error) {
	if f, ok := t.files[p]; ok {
		return f.ContentMD, nil
	}
	f, err := t.s.Repos.ReadFile(ctx, t.repo, p, t.baseSH)
	if err != nil {
		return "", err
	}
	return f.Content, nil
}

// incoming lists links in the revision's view that point at p: published
// links from pages the revision doesn't change, plus links in pages it does.
func (s *Service) incoming(ctx context.Context, repo repos.Repo, t *revisionTree, p string) ([]Inbound, error) {
	pub, err := s.inbound(ctx, repo.ID, p)
	if err != nil {
		return nil, err
	}
	out := []Inbound{}
	for _, in := range pub {
		if _, changed := t.files[in.From]; !changed && !t.gone[in.From] {
			out = append(out, in)
		}
	}
	for fp, f := range t.files {
		if fp == p || !repos.IsMarkdown(fp) {
			continue
		}
		ls, _, err := s.Engine.Links(ctx, f.ContentMD)
		if err != nil {
			continue
		}
		for _, l := range ls {
			tg := Resolve(repo, fp, l.URL)
			if tg.Kind == KindExternal || tg.Kind == KindAnchor {
				continue
			}
			for _, c := range candidates(tg.Path) {
				if c == p {
					out = append(out, Inbound{From: fp, URL: l.URL, Anchor: tg.Anchor, Line: l.Line})
					break
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].From < out[j].From || out[i].From == out[j].From && out[i].Line < out[j].Line
	})
	return out, nil
}

// ForRevision returns a page's links as the revision sees it.
func (s *Service) ForRevision(ctx context.Context, repo repos.Repo, rev revisions.Revision, p string) (PageLinks, error) {
	t, err := s.revisionTree(ctx, repo, rev)
	if err != nil {
		return PageLinks{}, err
	}
	md, err := t.content(ctx, p)
	if err != nil {
		return PageLinks{}, err
	}
	out, err := s.check(ctx, repo, t, p, md)
	if err != nil {
		return PageLinks{}, err
	}
	in, err := s.incoming(ctx, repo, t, p)
	if err != nil {
		return PageLinks{}, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Broken != "" && out[j].Broken == "" })
	return PageLinks{Outgoing: out, Incoming: in}, nil
}

// BrokenLink is a problem reported by revision checks.
type BrokenLink struct {
	Path   string `json:"path"` // page containing the link
	Line   int    `json:"line"`
	URL    string `json:"url"`
	Reason string `json:"reason"`           // missing_page | missing_anchor
	Target string `json:"target,omitempty"` // page the link was meant for
}

// Checks is the pre-review report for a revision.
type Checks struct {
	// BrokenLinks are links in pages the revision changes that don't resolve.
	BrokenLinks []BrokenLink `json:"broken_links"`
	// BreaksInbound are published links elsewhere that the revision's deletes
	// and renames would break.
	BreaksInbound []BrokenLink `json:"breaks_inbound"`
}

// Check reports broken links for a revision.
func (s *Service) Check(ctx context.Context, repo repos.Repo, rev revisions.Revision) (Checks, error) {
	t, err := s.revisionTree(ctx, repo, rev)
	if err != nil {
		return Checks{}, err
	}
	out := Checks{BrokenLinks: []BrokenLink{}, BreaksInbound: []BrokenLink{}}
	paths := make([]string, 0, len(t.files))
	for p := range t.files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if !repos.IsMarkdown(p) {
			continue
		}
		links, err := s.check(ctx, repo, t, p, t.files[p].ContentMD)
		if err != nil {
			continue
		}
		for _, l := range links {
			if l.Broken != "" {
				out.BrokenLinks = append(out.BrokenLinks, BrokenLink{Path: p, Line: l.Line, URL: l.URL, Reason: l.Broken, Target: l.Path})
			}
		}
	}
	gone := make([]string, 0, len(t.gone))
	for p := range t.gone {
		if !t.Exists(p) {
			gone = append(gone, p)
		}
	}
	sort.Strings(gone)
	for _, p := range gone {
		in, err := s.inbound(ctx, repo.ID, p)
		if err != nil {
			return out, err
		}
		for _, l := range in {
			if _, changed := t.files[l.From]; changed || t.gone[l.From] {
				continue // pages in the revision are checked above
			}
			out.BreaksInbound = append(out.BreaksInbound, BrokenLink{Path: l.From, Line: l.Line, URL: l.URL, Reason: "missing_page", Target: p})
		}
	}
	return out, nil
}

// Rewrite is one link change a move needs.
type Rewrite struct {
	Path   string `json:"path"` // page whose link changes
	Line   int    `json:"line"`
	Before string `json:"before"`
	After  string `json:"after"`
}

func relativeTo(from, target string) string {
	fromDir := path.Dir(from)
	var fp []string
	if fromDir != "." {
		fp = strings.Split(fromDir, "/")
	}
	tp := strings.Split(target, "/")
	i := 0
	for i < len(fp) && i < len(tp)-1 && fp[i] == tp[i] {
		i++
	}
	parts := make([]string, 0, len(fp)-i+len(tp)-i)
	for range fp[i:] {
		parts = append(parts, "..")
	}
	return strings.Join(append(parts, tp[i:]...), "/")
}

// pathToRoute maps a repo path back to a site route with .kmdn.yml routes.
func pathToRoute(repo repos.Repo, p string) (string, bool) {
	bestPrefix, bestDst := "", ""
	for prefix, dst := range repo.KmdnYML.Routes {
		d := strings.TrimSuffix(dst, "/")
		if (p == d || strings.HasPrefix(p, d+"/")) && len(d) >= len(bestDst) {
			bestPrefix, bestDst = prefix, d
		}
	}
	if bestPrefix == "" {
		return "/" + p, false
	}
	return strings.TrimSuffix(bestPrefix, "/") + "/" + strings.TrimPrefix(strings.TrimPrefix(p, bestDst), "/"), true
}

// rehref writes a destination for newFile in the style of old.
func rehref(repo repos.Repo, from, old string, tg Target, newFile string) string {
	stem := path.Ext(strings.SplitN(strings.SplitN(old, "#", 2)[0], "?", 2)[0]) == ""
	dirStyle := strings.HasSuffix(strings.SplitN(old, "#", 2)[0], "/")
	target := newFile
	base := path.Base(newFile)
	var out string
	if tg.Kind == KindRoute {
		out, _ = pathToRoute(repo, target)
	} else {
		out = relativeTo(from, target)
		if strings.HasPrefix(old, "./") && !strings.HasPrefix(out, "../") {
			out = "./" + out
		}
	}
	switch {
	case dirStyle && (base == "index.md" || base == "README.md" || base == "_index.md"):
		out = strings.TrimSuffix(out, base)
		if out == "" || out == "./" {
			out = "./"
		}
	case stem:
		out = strings.TrimSuffix(out, path.Ext(out))
	}
	if tg.Anchor != "" {
		out += "#" + tg.Anchor
	}
	return out
}

// planRename computes, per page, the destination replacements that moving
// from → to needs: links to the page, and the moved page's own relative links.
func (s *Service) planRename(ctx context.Context, repo repos.Repo, rev revisions.Revision, from, to string) (map[string]map[string]string, []Rewrite, error) {
	t, err := s.revisionTree(ctx, repo, rev)
	if err != nil {
		return nil, nil, err
	}
	plans := map[string]map[string]string{}
	var list []Rewrite
	add := func(page, before, after string, line int) {
		if before == after {
			return
		}
		if plans[page] == nil {
			plans[page] = map[string]string{}
		}
		if _, dup := plans[page][before]; !dup {
			plans[page][before] = after
		}
		list = append(list, Rewrite{Path: page, Line: line, Before: before, After: after})
	}
	// Links to the page from elsewhere. Before the rename is applied, pages
	// still see it at from; afterwards the manifest has it at to, so look
	// for links to either.
	for _, target := range []string{from, to} {
		in, err := s.incoming(ctx, repo, t, target)
		if err != nil {
			return nil, nil, err
		}
		for _, l := range in {
			if l.From == from || l.From == to {
				continue
			}
			tg := Resolve(repo, l.From, l.URL)
			add(l.From, l.URL, rehref(repo, l.From, l.URL, tg, to), l.Line)
		}
	}
	// The moved page's own relative links.
	own := from
	if !t.Exists(from) && t.Exists(to) {
		own = to
	}
	if md, err := t.content(ctx, own); err == nil {
		ls, _, err := s.Engine.Links(ctx, md)
		if err != nil {
			return nil, nil, err
		}
		for _, l := range ls {
			tg := Resolve(repo, from, l.URL)
			if tg.Kind != KindRelative {
				continue // routes and anchors don't depend on the page's folder
			}
			file := tg.Path
			for _, c := range candidates(tg.Path) {
				if t.Exists(c) || c == from {
					file = c
					break
				}
			}
			if file == from {
				file = to // a link to itself
			}
			add(to, l.URL, rehref(repo, to, l.URL, tg, file), l.Line)
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Path < list[j].Path })
	return plans, list, nil
}

// RenamePreview lists the link changes moving from → to needs.
func (s *Service) RenamePreview(ctx context.Context, repo repos.Repo, rev revisions.Revision, from, to string) ([]Rewrite, error) {
	_, list, err := s.planRename(ctx, repo, rev, from, to)
	if list == nil {
		list = []Rewrite{}
	}
	return list, err
}

// RewriteAfterRename updates links for a page that moved from → to (call it
// after the rename). Pages not yet in the revision are added to it.
func (s *Service) RewriteAfterRename(ctx context.Context, apply Applier, repo repos.Repo, rev revisions.Revision, c revisions.Caller, from, to string) (int, error) {
	ctx, current, unlock, gateErr := s.Revisions.Mutate(ctx, rev.ID)
	if gateErr != nil {
		return 0, gateErr
	}
	defer unlock()
	rev = current
	plans, _, err := s.planRename(ctx, repo, rev, from, to)
	if err != nil {
		return 0, err
	}
	t, err := s.revisionTree(ctx, repo, rev)
	if err != nil {
		return 0, err
	}
	n := 0
	pages := make([]string, 0, len(plans))
	for p := range plans {
		pages = append(pages, p)
	}
	sort.Strings(pages)
	for _, p := range pages {
		md, err := t.content(ctx, p)
		if err != nil {
			return n, err
		}
		next, err := s.Engine.RewriteLinks(ctx, md, plans[p])
		if err != nil {
			return n, err
		}
		if next == md {
			continue
		}
		if err := apply.Apply(ctx, repo, rev, c, p, next, "links"); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
