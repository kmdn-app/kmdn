package app

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kmdn-app/kmdn/internal/collab"
	"github.com/kmdn-app/kmdn/internal/users"
)

func TestLinksChecksAndRenameRewriting(t *testing.T) {
	a, admin := newApp(t, nil)
	a.Collab.Options = collab.Options{FlushDelay: time.Millisecond, QuietPeriod: time.Millisecond}
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	repoID := connectLocal(t, a, admin, map[string]string{
		".kmdn.yml":                 "routes:\n  /docs/: docs/\n",
		"docs/index.md":             "# Handbook\n\nRead the [first week](guides/first-week.md) and [setup](/docs/guides/setup).\n",
		"docs/guides/first-week.md": "# First week\n\n## Day one\n\nBack to [the handbook](../index.md#handbook) or [setup](setup.md#install).\n",
		"docs/guides/setup.md":      "# Setup\n\n## Install\n\nSee [day one](first-week.md#day-one), [nowhere](missing.md) and [bad anchor](first-week.md#nope).\n",
	})

	// Published: outgoing links are checked, backlinks come from the index.
	code, pl := admin.do("GET", "/repos/"+repoID+"/links/docs/guides/setup.md", nil)
	if code != 200 {
		t.Fatalf("links: %d %v", code, pl)
	}
	var broken []string
	for _, o := range pl["outgoing"].([]any) {
		if b, _ := o.(map[string]any)["broken"].(string); b != "" {
			broken = append(broken, o.(map[string]any)["url"].(string)+":"+b)
		}
	}
	if fmt.Sprint(broken) != "[missing.md:missing_page first-week.md#nope:missing_anchor]" {
		t.Fatalf("broken: %v", broken)
	}
	var from []string
	for _, in := range pl["incoming"].([]any) {
		from = append(from, in.(map[string]any)["from_path"].(string))
	}
	if fmt.Sprint(from) != "[docs/guides/first-week.md docs/index.md]" {
		t.Fatalf("incoming: %v", from)
	}

	// A revision moves first-week.md into onboarding/.
	_, rev := admin.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Restructure"})
	revID := rev["id"].(string)
	code, pv := admin.do("POST", "/revisions/"+revID+"/files/rename-preview", map[string]any{"from": "docs/guides/first-week.md", "to": "docs/onboarding/first-week.md"})
	if code != 200 {
		t.Fatalf("preview: %d %v", code, pv)
	}
	var rw []string
	for _, it := range pv["items"].([]any) {
		m := it.(map[string]any)
		rw = append(rw, fmt.Sprintf("%s: %s → %s", m["path"], m["before"], m["after"]))
	}
	want := []string{
		"docs/guides/setup.md: first-week.md#day-one → ../onboarding/first-week.md#day-one",
		"docs/guides/setup.md: first-week.md#nope → ../onboarding/first-week.md#nope",
		"docs/index.md: guides/first-week.md → onboarding/first-week.md",
		"docs/onboarding/first-week.md: setup.md#install → ../guides/setup.md#install",
	}
	if strings.Join(rw, "\n") != strings.Join(want, "\n") {
		t.Fatalf("preview:\n%s\nwant:\n%s", strings.Join(rw, "\n"), strings.Join(want, "\n"))
	}
	// The move alone breaks the inbound links, and the moved page's own
	// relative link…
	admin.do("POST", "/revisions/"+revID+"/files", map[string]any{"op": "rename", "from_path": "docs/guides/first-week.md", "path": "docs/onboarding/first-week.md"})
	_, ch := admin.do("GET", "/revisions/"+revID+"/checks", nil)
	if n, own := len(ch["breaks_inbound"].([]any)), len(ch["broken_links"].([]any)); n != 3 || own != 1 {
		t.Fatalf("after the move: %v", ch)
	}
	// …until they're rewritten.
	if code, res := admin.do("POST", "/revisions/"+revID+"/links/rewrite", map[string]any{"from": "docs/guides/first-week.md", "to": "docs/onboarding/first-week.md"}); code != 200 || res["pages"] != float64(3) {
		t.Fatalf("rewrite: %d %v", code, res)
	}
	a.Collab.Flush(ctx)
	read := func(p string) string {
		_, c := admin.do("GET", "/revisions/"+revID+"/files/"+p, nil)
		s, _ := c["content"].(string)
		return s
	}
	if got := read("docs/index.md"); got != "# Handbook\n\nRead the [first week](onboarding/first-week.md) and [setup](/docs/guides/setup).\n" {
		t.Fatalf("index after rewrite: %q", got)
	}
	if got := read("docs/onboarding/first-week.md"); got != "# First week\n\n## Day one\n\nBack to [the handbook](../index.md#handbook) or [setup](../guides/setup.md#install).\n" {
		t.Fatalf("moved page after rewrite: %q", got)
	}
	_, ch = admin.do("GET", "/revisions/"+revID+"/checks", nil)
	if n := len(ch["breaks_inbound"].([]any)); n != 0 {
		t.Fatalf("still breaks inbound: %v", ch)
	}
	var still []string
	for _, b := range ch["broken_links"].([]any) {
		m := b.(map[string]any)
		still = append(still, m["path"].(string)+" "+m["url"].(string))
	}
	if fmt.Sprint(still) != "[docs/guides/setup.md missing.md docs/guides/setup.md first-week.md#nope]" && fmt.Sprint(still) != "[docs/guides/setup.md missing.md docs/guides/setup.md ../onboarding/first-week.md#nope]" {
		t.Fatalf("broken links in changed pages: %v", still)
	}
	// In the revision, the moved page's backlinks follow it.
	_, rl := admin.do("GET", "/revisions/"+revID+"/links/docs/onboarding/first-week.md", nil)
	if n := len(rl["incoming"].([]any)); n != 3 {
		t.Fatalf("revision backlinks: %v", rl)
	}
}
