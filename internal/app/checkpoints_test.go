package app

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/collab"
	"github.com/kmdn-app/kmdn/internal/realtime"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/users"
)

func TestCheckpointsAndRestore(t *testing.T) {
	a, admin := newApp(t, nil)
	a.Collab.Options = collab.Options{FlushDelay: time.Millisecond, QuietPeriod: time.Millisecond}
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	repoID := connectLocal(t, a, admin, map[string]string{
		"docs/index.md": "# Handbook\n\nStart here.\n",
		"docs/old.md":   "# Old\n",
	})
	sam, _ := users.Create(ctx, a.DB, "sam@northwind.dev", "Sam", false)
	_ = access.Grant(ctx, a.DB, repoID, "user", sam.ID, access.Contributor)
	samC := &tc{t: t, base: admin.base, c: newClient()}
	signIn(t, a, samC, sam)
	_, revJSON := samC.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Refresh"})
	revID := revJSON["id"].(string)
	rev, _ := revisions.Get(ctx, a.DB, revID)
	repo, _ := repos.Get(ctx, a.DB, repoID)
	caller := revisions.Caller{User: sam, Role: access.Contributor}

	read := func(p string) (int, string) {
		code, c := samC.do("GET", "/revisions/"+revID+"/files/"+p, nil)
		s, _ := c["content"].(string)
		return code, s
	}
	manifest := func() string {
		_, files := samC.do("GET", "/revisions/"+revID+"/files", nil)
		return fmt.Sprint(paths(files["items"]))
	}

	// State at the checkpoint: index.md edited.
	if err := a.Collab.Apply(ctx, repo, rev, caller, "docs/index.md", "# Handbook\n\nStart here. Version one.\n", "human"); err != nil {
		t.Fatal(err)
	}
	code, cp := samC.do("POST", "/revisions/"+revID+"/checkpoints", map[string]any{"name": "First draft"})
	if code != 201 || cp["kind"] != "named" || cp["file_count"] != float64(1) {
		t.Fatalf("name checkpoint: %d %v", code, cp)
	}
	cpID := cp["id"].(string)
	if code, f := samC.do("GET", "/revisions/"+revID+"/checkpoints/"+cpID+"/files/docs/index.md", nil); code != 200 || f["content"] != "# Handbook\n\nStart here. Version one.\n" {
		t.Fatalf("checkpoint file: %d %v", code, f)
	}

	// Later changes: another edit, a new page, a deleted page.
	_ = a.Collab.Apply(ctx, repo, rev, caller, "docs/index.md", "# Handbook\n\nRewritten entirely.\n", "human")
	samC.do("POST", "/revisions/"+revID+"/files", map[string]any{"op": "add", "path": "docs/new.md", "content": "# New\n"})
	samC.do("POST", "/revisions/"+revID+"/files", map[string]any{"op": "delete", "path": "docs/old.md"})
	a.Collab.Flush(ctx)
	if got := manifest(); got != "[docs/index.md:modify docs/new.md:add docs/old.md:delete]" {
		t.Fatalf("before restore: %s", got)
	}

	// An editor has the page open while the restore happens.
	w := dialWS(t, a, samC)
	w.control(map[string]any{"op": "subscribe", "channel": 1, "room": map[string]any{"revision": revID, "path": "docs/index.md"}})
	w.op("subscribed")

	if code, body := samC.do("POST", "/revisions/"+revID+"/checkpoints/"+cpID+"/restore", nil); code != 204 {
		t.Fatalf("restore: %d %v", code, body)
	}
	w.next("restore update", func(f frame) bool { return f.kind == realtime.KindSync && f.payload[0] == 2 })

	if _, c := read("docs/index.md"); c != "# Handbook\n\nStart here. Version one.\n" {
		t.Fatalf("restored content: %q", c)
	}
	if code, _ := read("docs/new.md"); code != 404 {
		t.Fatalf("page added after the checkpoint should be gone: %d", code)
	}
	if code, c := read("docs/old.md"); code != 200 || c != "# Old\n" {
		t.Fatalf("page deleted after the checkpoint should be back: %d %q", code, c)
	}
	_, list := samC.do("GET", "/revisions/"+revID+"/checkpoints", nil)
	var kinds []string
	for _, it := range list["items"].([]any) {
		kinds = append(kinds, it.(map[string]any)["kind"].(string))
	}
	if fmt.Sprint(kinds) != "[pre_restore named]" {
		t.Fatalf("checkpoints: %v", kinds)
	}
	// Restoring the pre-restore checkpoint undoes the restore.
	preID := list["items"].([]any)[0].(map[string]any)["id"].(string)
	if code, _ := samC.do("POST", "/revisions/"+revID+"/checkpoints/"+preID+"/restore", nil); code != 204 {
		t.Fatalf("undo restore: %d", code)
	}
	if _, c := read("docs/index.md"); c != "# Handbook\n\nRewritten entirely.\n" {
		t.Fatalf("after undo: %q", c)
	}
	if got := manifest(); got != "[docs/index.md:modify docs/new.md:add docs/old.md:delete]" {
		t.Fatalf("manifest after undo: %s", got)
	}
}
