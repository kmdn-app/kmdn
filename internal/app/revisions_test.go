package app

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/users"
)

// connectLocal connects a plain git repo holding files and syncs it. The
// caller must be signed in as an instance admin.
func connectLocal(t *testing.T, a *App, admin *tc, files map[string]string) string {
	t.Helper()
	ctx := context.Background()
	remote := t.TempDir()
	gitIn(t, remote, "init", "--quiet", "-b", "main")
	gitIn(t, remote, "config", "uploadpack.allowFilter", "true")
	gitIn(t, remote, "config", "uploadpack.allowAnySHA1InWant", "true")
	for p, c := range files {
		writeFile(t, remote, p, c)
	}
	gitIn(t, remote, "add", "-A")
	gitIn(t, remote, "commit", "--quiet", "-m", "Initial")
	_, hosts := admin.do("GET", "/admin/forges", nil)
	hostID := hosts["items"].([]any)[0].(map[string]any)["id"].(string)
	code, body := admin.do("POST", "/repos", map[string]any{"forge_host_id": hostID, "clone_url": "file://" + remote, "content_root": "docs/"})
	if code != 202 {
		t.Fatalf("connect: %d %v", code, body)
	}
	for {
		ran, err := a.Jobs.RunOnce(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !ran {
			break
		}
	}
	return body["repo"].(map[string]any)["id"].(string)
}

func paths(v any) []string {
	var out []string
	for _, it := range v.([]any) {
		m := it.(map[string]any)
		s := m["path"].(string)
		if op, ok := m["op"].(string); ok && op != "" {
			s += ":" + op
		}
		out = append(out, s)
	}
	return out
}

func TestRevisionLifecycleAndManifest(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	repoID := connectLocal(t, a, admin, map[string]string{
		"docs/index.md":             "# Handbook\n\nStart here.\n",
		"docs/guides/first-week.md": "# First week\n\nWelcome.\n",
		"docs/guides/old.md":        "# Old\n",
		".kmdn/templates/how-to.md": "# How to …\n\n## Steps\n",
	})

	// A contributor and a viewer.
	sam, _ := users.Create(ctx, a.DB, "sam@northwind.dev", "Sam", false)
	vic, _ := users.Create(ctx, a.DB, "vic@northwind.dev", "Vic", false)
	if err := access.Grant(ctx, a.DB, repoID, "user", sam.ID, access.Contributor); err != nil {
		t.Fatal(err)
	}
	if err := access.Grant(ctx, a.DB, repoID, "user", vic.ID, access.Viewer); err != nil {
		t.Fatal(err)
	}
	samC := &tc{t: t, base: admin.base, c: newClient()}
	signIn(t, a, samC, sam)
	vicC := &tc{t: t, base: admin.base, c: newClient()}
	signIn(t, a, vicC, vic)

	if code, _ := vicC.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Nope"}); code != 403 {
		t.Fatalf("viewer create: %d", code)
	}
	if code, body := samC.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": " "}); code != 422 {
		t.Fatalf("empty title: %d %v", code, body)
	}

	// The first keystroke in Published: a revision with the page already in it.
	code, rev := samC.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Edits to First week", "path": "docs/guides/first-week.md"})
	if code != 201 || rev["number"] != float64(1) || rev["state"] != "editing" || rev["file_count"] != float64(1) {
		t.Fatalf("create: %d %v", code, rev)
	}
	if acc := rev["access"].(map[string]any); acc["can_edit"] != true || acc["member"] != true {
		t.Fatalf("access: %v", acc)
	}
	revID := rev["id"].(string)
	if code, byNum := vicC.do("GET", "/repos/"+repoID+"/revisions/by-number/1", nil); code != 200 || byNum["id"] != revID || byNum["access"].(map[string]any)["reason"] != "viewer" {
		t.Fatalf("by number (viewer): %d %v", code, byNum)
	}

	// Reading inside the revision: touched page from the manifest, untouched from base.
	code, c := samC.do("GET", "/revisions/"+revID+"/files/docs%2Fguides%2Ffirst-week.md", nil)
	if code != 200 || c["in_revision"] != true || c["op"] != "modify" || c["content"] != "# First week\n\nWelcome.\n" {
		t.Fatalf("read touched: %d %v", code, c)
	}
	if code, c := samC.do("GET", "/revisions/"+revID+"/files/docs/index.md", nil); code != 200 || c["in_revision"] != false || c["content"] != "# Handbook\n\nStart here.\n" {
		t.Fatalf("read untouched: %d %v", code, c)
	}

	op := func(cl *tc, body map[string]any) (int, map[string]any) {
		return cl.do("POST", "/revisions/"+revID+"/files", body)
	}
	// Add from a template, rename, delete.
	if code, f := op(samC, map[string]any{"op": "add", "path": "docs/guides/deploy.md", "template": ".kmdn/templates/how-to.md"}); code != 200 || f["op"] != "add" || f["additions"] != float64(3) {
		t.Fatalf("add: %d %v", code, f)
	}
	if code, _ := op(samC, map[string]any{"op": "add", "path": "docs/index.md"}); code != 409 {
		t.Fatalf("add over published: %d", code)
	}
	if code, _ := op(samC, map[string]any{"op": "add", "path": "src/app.md"}); code != 422 {
		t.Fatalf("add out of scope: %d", code)
	}
	if code, f := op(samC, map[string]any{"op": "rename", "from_path": "docs/index.md", "path": "docs/start.md"}); code != 200 || f["op"] != "rename" || f["from_path"] != "docs/index.md" {
		t.Fatalf("rename: %d %v", code, f)
	}
	if code, body := samC.do("GET", "/revisions/"+revID+"/files/docs/index.md", nil); code != 409 || body["code"] != "file_renamed" {
		t.Fatalf("read renamed-away: %d %v", code, body)
	}
	if code, f := op(samC, map[string]any{"op": "rename", "from_path": "docs/guides/deploy.md", "path": "docs/guides/deploying.md"}); code != 200 || f["op"] != "add" {
		t.Fatalf("rename an added page: %d %v", code, f)
	}
	if code, f := op(samC, map[string]any{"op": "delete", "path": "docs/guides/old.md"}); code != 200 || f["op"] != "delete" || f["deletions"] != float64(1) {
		t.Fatalf("delete: %d %v", code, f)
	}
	if code, _ := vicC.do("POST", "/revisions/"+revID+"/files", map[string]any{"op": "delete", "path": "docs/guides/first-week.md"}); code != 403 {
		t.Fatalf("viewer file op: %d", code)
	}

	_, files := samC.do("GET", "/revisions/"+revID+"/files", nil)
	got := fmt.Sprint(paths(files["items"]))
	if want := "[docs/guides/deploying.md:add docs/guides/first-week.md:modify docs/guides/old.md:delete docs/start.md:rename]"; got != want {
		t.Fatalf("manifest %s, want %s", got, want)
	}
	_, tree := samC.do("GET", "/revisions/"+revID+"/tree", nil)
	got = fmt.Sprint(paths(tree["items"]))
	if want := "[docs/guides docs/guides/deploying.md:add docs/guides/first-week.md:modify docs/start.md:rename]"; got != want {
		t.Fatalf("tree %s, want %s", got, want)
	}

	// Undo steps collapse back: rename back, delete the added page.
	if code, f := op(samC, map[string]any{"op": "rename", "from_path": "docs/start.md", "path": "docs/index.md"}); code != 200 || f["op"] != "modify" {
		t.Fatalf("rename back: %d %v", code, f)
	}
	if code, _ := op(samC, map[string]any{"op": "delete", "path": "docs/guides/deploying.md"}); code != 200 {
		t.Fatalf("delete added: %d", code)
	}
	if code, f := op(samC, map[string]any{"op": "add", "path": "docs/guides/old.md", "content": "# Old, revived\n"}); code != 200 || f["op"] != "modify" {
		t.Fatalf("re-add deleted: %d %v", code, f)
	}
	_, files = samC.do("GET", "/revisions/"+revID+"/files", nil)
	if got, want := fmt.Sprint(paths(files["items"])), "[docs/guides/first-week.md:modify docs/guides/old.md:modify docs/index.md:modify]"; got != want {
		t.Fatalf("manifest after undo %s, want %s", got, want)
	}

	// Members: only repo contributors can be added.
	if code, body := samC.do("PUT", "/revisions/"+revID+"/members/"+vic.ID, nil); code != 422 {
		t.Fatalf("add viewer as editor: %d %v", code, body)
	}
	tom, _ := users.Create(ctx, a.DB, "tom@northwind.dev", "Tom", false)
	if err := access.Grant(ctx, a.DB, repoID, "user", tom.ID, access.Contributor); err != nil {
		t.Fatal(err)
	}
	tomC := &tc{t: t, base: admin.base, c: newClient()}
	signIn(t, a, tomC, tom)
	if code, _ := op(tomC, map[string]any{"op": "modify", "path": "docs/index.md"}); code != 403 {
		t.Fatalf("non-member contributor edit: %d", code)
	}
	if code, ms := samC.do("PUT", "/revisions/"+revID+"/members/"+tom.ID, nil); code != 200 || len(ms["items"].([]any)) != 2 {
		t.Fatalf("add member: %d %v", code, ms)
	}
	if code, _ := op(tomC, map[string]any{"op": "modify", "path": "docs/index.md"}); code != 200 {
		t.Fatalf("member edit: %d", code)
	}
	if code, _ := tomC.do("DELETE", "/revisions/"+revID+"/members/"+sam.ID, nil); code != 403 && code != 409 {
		t.Fatalf("remove owner: %d", code)
	}
	// Maintainers (the admin) can edit any Editing revision.
	if code, _ := op(admin, map[string]any{"op": "modify", "path": "docs/index.md"}); code != 200 {
		t.Fatalf("maintainer edit: %d", code)
	}

	// Listing and metadata.
	if code, l := vicC.do("GET", "/repos/"+repoID+"/revisions", nil); code != 200 || len(l["items"].([]any)) != 1 {
		t.Fatalf("list: %d %v", code, l)
	}
	if code, l := vicC.do("GET", "/repos/"+repoID+"/revisions?mine=true", nil); code != 200 || len(l["items"].([]any)) != 0 {
		t.Fatalf("list mine: %d %v", code, l)
	}
	if code, r := samC.do("PATCH", "/revisions/"+revID, map[string]any{"title": "Onboarding refresh", "description": "Updates for Q3."}); code != 200 || r["title"] != "Onboarding refresh" {
		t.Fatalf("patch: %d %v", code, r)
	}

	// Close and reopen.
	if code, r := samC.do("POST", "/revisions/"+revID+"/close", nil); code != 200 || r["state"] != "closed" || r["access"].(map[string]any)["can_edit"] != false {
		t.Fatalf("close: %d %v", code, r)
	}
	if code, _ := op(samC, map[string]any{"op": "modify", "path": "docs/guides/first-week.md"}); code != 403 {
		t.Fatalf("edit closed: %d", code)
	}
	if code, l := samC.do("GET", "/repos/"+repoID+"/revisions", nil); code != 200 || len(l["items"].([]any)) != 0 {
		t.Fatalf("closed not in open list: %v", l)
	}
	if code, r := samC.do("POST", "/revisions/"+revID+"/reopen", nil); code != 200 || r["state"] != "editing" {
		t.Fatalf("reopen: %d %v", code, r)
	}

	_, ev := samC.do("GET", "/revisions/"+revID+"/events", nil)
	var kinds []string
	for _, e := range ev["items"].([]any) {
		kinds = append(kinds, e.(map[string]any)["kind"].(string))
	}
	if got := strings.Join(kinds, ","); got != "created,file_added,file_renamed,file_renamed,file_deleted,file_renamed,file_deleted,file_added,member_added,updated,closed,reopened" {
		t.Fatalf("events: %s", got)
	}

	// Numbers are per repo and increase.
	if code, r2 := samC.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Second"}); code != 201 || r2["number"] != float64(2) {
		t.Fatalf("second: %d %v", code, r2)
	}
	if code, tpl := samC.do("GET", "/repos/"+repoID+"/templates", nil); code != 200 || fmt.Sprint(paths(tpl["items"])) != "[.kmdn/templates/how-to.md]" {
		t.Fatalf("templates: %d %v", code, tpl)
	}
}
