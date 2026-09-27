package app

import (
	"context"
	"strings"
	"testing"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

func TestUpdatesFromPublished(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	const index = "# Guide\n\nIntro.\n\n## Setup\n\nInstall.\n\n## Usage\n\nRun it.\n"
	repoID, remote := connectLocalRemote(t, a, admin, map[string]string{"docs/index.md": index, "docs/gone.md": "# Gone\n\nSoon.\n", "docs/other.md": "# Other\n"})
	sam, _ := users.Create(ctx, a.DB, "sam@northwind.dev", "Sam", false)
	_ = access.Grant(ctx, a.DB, repoID, "user", sam.ID, access.Contributor)
	samC := &tc{t: t, base: admin.base, c: newClient()}
	signIn(t, a, samC, sam)
	a.Collab.Options.QuietPeriod = 0

	_, rev := samC.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Setup docs", "path": "docs/index.md"})
	revID := rev["id"].(string)
	rv, _ := revisions.Get(ctx, a.DB, revID)
	repo, _ := repos.Get(ctx, a.DB, repoID)
	caller := revisions.Caller{User: sam, Role: access.Contributor}
	if err := a.Collab.Apply(ctx, repo, rv, caller, "docs/index.md", strings.Replace(index, "Install.", "Install with brew.", 1), "human"); err != nil {
		t.Fatal(err)
	}
	if err := a.Collab.Apply(ctx, repo, rv, caller, "docs/gone.md", "# Gone\n\nStill needed.\n", "human"); err != nil {
		t.Fatal(err)
	}
	a.Collab.FlushRevision(ctx, revID)
	content := func(p string) string {
		var md string
		_ = store.QueryRow(ctx, a.DB, `SELECT content_md FROM revision_files WHERE revision_id = ? AND path = ?`, revID, p).Scan(&md)
		return md
	}
	baseSHA := func() string {
		r, _ := revisions.Get(ctx, a.DB, revID)
		return r.BaseSHA
	}
	push := func(msg string, edit func()) string {
		edit()
		gitIn(t, remote, "add", "-A")
		gitIn(t, remote, "commit", "--quiet", "-m", msg)
		if err := a.Repos.Sync(ctx, repoID); err != nil {
			t.Fatal(err)
		}
		runJobs(t, a)
		return gitIn(t, remote, "rev-parse", "HEAD")
	}

	// Published edits another section of index.md and deletes gone.md.
	head := push("Edit on the forge", func() {
		writeFile(t, remote, "docs/index.md", strings.Replace(index, "Run it.", "Run it daily.", 1))
		gitIn(t, remote, "rm", "--quiet", "docs/gone.md")
	})
	code, body := samC.do("GET", "/revisions/"+revID+"/updates", nil)
	up, _ := body["update"].(map[string]any)
	if code != 200 || up == nil || body["can_apply"] != true || up["target_sha"] != head || up["conflicts"] != float64(1) {
		t.Fatalf("pending update: %d %v", code, body)
	}
	kinds := map[string]string{}
	for _, f := range up["files"].([]any) {
		m := f.(map[string]any)
		kinds[m["path"].(string)] = m["kind"].(string)
	}
	if kinds["docs/index.md"] != "merge" || kinds["docs/gone.md"] != "deleted_upstream" {
		t.Fatalf("files: %v", kinds)
	}
	if lc, _ := up["last_commit"].(map[string]any); lc == nil || lc["title"] != "Edit on the forge" {
		t.Fatalf("last commit: %v", up["last_commit"])
	}
	if code, _ := samC.do("POST", "/revisions/"+revID+"/updates/upd_nope/apply", nil); code != 409 {
		t.Fatalf("stale update id: %d", code)
	}

	code, res := samC.do("POST", "/revisions/"+revID+"/updates/"+up["id"].(string)+"/apply", nil)
	if code != 200 || res["files"] != float64(2) || res["conflicts"] != float64(1) {
		t.Fatalf("apply: %d %v", code, res)
	}
	a.Collab.FlushRevision(ctx, revID)
	if got := content("docs/index.md"); got != strings.Replace(strings.Replace(index, "Install.", "Install with brew.", 1), "Run it.", "Run it daily.", 1) {
		t.Fatalf("merged: %q", got)
	}
	if baseSHA() != head {
		t.Fatalf("base didn't move: %s", baseSHA())
	}
	var conflictKind string
	_ = store.QueryRow(ctx, a.DB, `SELECT conflict FROM revision_files WHERE revision_id = ? AND path = 'docs/gone.md'`, revID).Scan(&conflictKind)
	if conflictKind != "deleted_upstream" {
		t.Fatalf("deleted upstream: %q", conflictKind)
	}
	if _, body := samC.do("GET", "/revisions/"+revID+"/updates", nil); body["update"] != nil {
		t.Fatalf("still pending: %v", body)
	}
	// The sync client isn't credited.
	var syncClients int
	_ = store.QueryRow(ctx, a.DB, `SELECT COUNT(*) FROM ydoc_clients c JOIN ydocs d ON d.id = c.ydoc_id WHERE d.revision_id = ? AND c.kind = 'sync'`, revID).Scan(&syncClients)
	if syncClients == 0 {
		t.Fatal("no sync client recorded")
	}

	// A change to a page the revision doesn't touch just moves the base.
	head = push("Other page", func() { writeFile(t, remote, "docs/other.md", "# Other\n\nMore.\n") })
	if baseSHA() != head {
		t.Fatalf("no fast-forward: %s vs %s", baseSHA(), head)
	}
	if _, body := samC.do("GET", "/revisions/"+revID+"/updates", nil); body["update"] != nil {
		t.Fatalf("fast-forward left an update: %v", body)
	}

	// Both sides change the same line: a conflict block, the page keeps the revision's text.
	push("Clash", func() {
		writeFile(t, remote, "docs/index.md", strings.Replace(strings.Replace(index, "Run it.", "Run it daily.", 1), "Install.", "Install with apt.", 1))
	})
	_, body = samC.do("GET", "/revisions/"+revID+"/updates", nil)
	up = body["update"].(map[string]any)
	if up["conflicts"] != float64(1) {
		t.Fatalf("clash: %v", up)
	}
	if code, res := samC.do("POST", "/revisions/"+revID+"/updates/"+up["id"].(string)+"/apply", nil); code != 200 || res["conflicts"] != float64(1) {
		t.Fatalf("apply clash: %d %v", code, res)
	}
	a.Collab.FlushRevision(ctx, revID)
	if got := content("docs/index.md"); !strings.Contains(got, "Install with brew.") || strings.Contains(got, "apt") {
		t.Fatalf("conflicted page: %q", got)
	}
	_, state, _ := a.Collab.StateOf(ctx, revID, "docs/index.md")
	doc, _ := a.Engine.YReadDoc(ctx, state)
	if !strings.Contains(string(doc), `"conflict"`) || !strings.Contains(string(doc), "Install with apt.") {
		t.Fatalf("no conflict block: %s", doc)
	}
	if r, _ := revisions.Get(ctx, a.DB, revID); !r.HasConflicts {
		t.Fatal("revision not flagged")
	}
}
