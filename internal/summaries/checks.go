package summaries

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
)

// Finding is something a reviewer may want to look at. Advisory only.
type Finding struct {
	// broken_link | breaks_inbound | missing_alt | heading_jump | frontmatter | style
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Line    int    `json:"line,omitempty"`
	Message string `json:"message"`
	// Quote is the passage a style finding is about.
	Quote string `json:"quote,omitempty"`
}

var (
	missingAlt = regexp.MustCompile(`!\[\s*\]\(`)
	fmKey      = regexp.MustCompile(`^([A-Za-z0-9_-]+)\s*:`)
)

// frontmatterKeys returns the top-level YAML front matter keys of a page.
func frontmatterKeys(md string) map[string]bool {
	keys := map[string]bool{}
	lines := strings.Split(md, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return keys
	}
	for _, l := range lines[1:] {
		if strings.TrimSpace(l) == "---" {
			break
		}
		if m := fmKey.FindStringSubmatch(l); m != nil {
			keys[m[1]] = true
		}
	}
	return keys
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Checks finds problems in the revision's changed pages without AI:
// broken links and anchors, images without alt text, heading levels that
// skip, and front matter keys removed or missing compared to the folder.
func (s *Service) Checks(ctx context.Context, repo repos.Repo, rev revisions.Revision) ([]Finding, error) {
	s.Docs.FlushRevision(ctx, rev.ID)
	out := []Finding{}
	if lc, err := s.Links.Check(ctx, repo, rev); err == nil {
		for _, b := range lc.BrokenLinks {
			msg := fmt.Sprintf("The link to %s goes nowhere.", b.URL)
			if b.Reason == "missing_anchor" {
				msg = fmt.Sprintf("The link %s points at a heading that doesn't exist.", b.URL)
			}
			out = append(out, Finding{Kind: "broken_link", Path: b.Path, Line: b.Line, Message: msg})
		}
		for _, b := range lc.BreaksInbound {
			out = append(out, Finding{Kind: "breaks_inbound", Path: b.Path, Line: b.Line, Message: fmt.Sprintf("%s links to %s, which this revision moves or deletes.", b.Path, b.Target)})
		}
	}
	files, err := revisions.Files(ctx, s.DB, rev.ID)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if f.Op == revisions.OpDelete || !repos.IsMarkdown(f.Path) {
			continue
		}
		md := f.ContentMD
		for i, l := range strings.Split(md, "\n") {
			if missingAlt.MatchString(l) {
				out = append(out, Finding{Kind: "missing_alt", Path: f.Path, Line: i + 1, Message: "An image has no alt text: screen readers can't describe it."})
			}
		}
		if _, headings, err := s.Engine.Links(ctx, md); err == nil {
			prev := 0
			for _, h := range headings {
				if prev > 0 && h.Depth > prev+1 {
					out = append(out, Finding{Kind: "heading_jump", Path: f.Path, Line: h.Line, Message: fmt.Sprintf("“%s” jumps from a level %d heading to level %d.", h.Text, prev, h.Depth)})
				}
				prev = h.Depth
			}
		}
		out = append(out, s.frontmatterFindings(ctx, repo, f)...)
	}
	return out, nil
}

// frontmatterFindings: keys the revision removes, and keys every other page
// in the folder has but this one lacks.
func (s *Service) frontmatterFindings(ctx context.Context, repo repos.Repo, f revisions.File) []Finding {
	var out []Finding
	now := frontmatterKeys(f.ContentMD)
	before := frontmatterKeys(f.BaseMD)
	if f.BaseMD != "" {
		for _, k := range sortedKeys(before) {
			if !now[k] {
				out = append(out, Finding{Kind: "frontmatter", Path: f.Path, Line: 1, Message: fmt.Sprintf("The front matter key “%s” is removed.", k)})
			}
		}
	}
	dir := path.Dir(f.Path)
	nodes, err := s.Repos.Tree(ctx, repo)
	if err != nil {
		return out
	}
	var common map[string]bool
	n := 0
	for _, nd := range nodes {
		if nd.Type != "file" || !nd.Markdown || nd.Path == f.Path || path.Dir(nd.Path) != dir {
			continue
		}
		if n >= 20 {
			break
		}
		sib, err := s.Repos.ReadFile(ctx, repo, nd.Path, "")
		if err != nil {
			continue
		}
		keys := frontmatterKeys(sib.Content)
		if common == nil {
			common = keys
		} else {
			for k := range common {
				if !keys[k] {
					delete(common, k)
				}
			}
		}
		n++
	}
	if n < 2 {
		return out // not enough siblings to call it a convention
	}
	for _, k := range sortedKeys(common) {
		if !now[k] && !before[k] { // a removed key is already reported
			out = append(out, Finding{Kind: "frontmatter", Path: f.Path, Line: 1, Message: fmt.Sprintf("Other pages in %s/ have “%s” in their front matter; this one doesn't.", dir, k)})
		}
	}
	return out
}
