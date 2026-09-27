package app

import (
	"context"
	"testing"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

func TestCommentThreads(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	repoID := connectLocal(t, a, admin, map[string]string{"docs/index.md": "# Handbook\n\nStart here.\n"})
	mk := func(email, name string, role access.Role) (users.User, *tc) {
		u, _ := users.Create(ctx, a.DB, email, name, false)
		_ = access.Grant(ctx, a.DB, repoID, "user", u.ID, role)
		c := &tc{t: t, base: admin.base, c: newClient()}
		signIn(t, a, c, u)
		return u, c
	}
	_, samC := mk("sam@northwind.dev", "Sam", access.Contributor)
	a.Collab.Options.QuietPeriod = 0
	_, vicC := mk("vic@northwind.dev", "Vic", access.Viewer)
	_, rev := samC.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Start", "path": "docs/index.md"})
	revID := rev["id"].(string)

	// The page has a document once someone edits it.
	rv, _ := revisions.Get(ctx, a.DB, revID)
	repo, _ := repos.Get(ctx, a.DB, repoID)
	samU, _ := users.ByEmail(ctx, a.DB, "sam@northwind.dev")
	if err := a.Collab.Apply(ctx, repo, rv, revisions.Caller{User: samU, Role: access.Contributor}, "docs/index.md", "# Handbook\n\nStart here, now.\n", "human"); err != nil {
		t.Fatal(err)
	}
	// A viewer's thread anchor is written into the document for them.
	if code, th := vicC.do("POST", "/revisions/"+revID+"/threads", map[string]any{"path": "docs/index.md", "anchor": map[string]any{"quote": "now"},
		"position": map[string]any{"start": map[string]any{"assoc": 0}, "end": map[string]any{"assoc": -1}}, "body": "Why now?"}); code != 201 {
		t.Fatalf("anchored thread: %d %v", code, th)
	}
	var kind string
	if err := store.QueryRow(ctx, a.DB, `SELECT c.kind FROM ydoc_clients c JOIN ydocs d ON d.id = c.ydoc_id JOIN users u ON u.id = c.user_id WHERE d.revision_id = ? AND u.email = 'vic@northwind.dev'`, revID).Scan(&kind); err != nil || kind != "comment" {
		t.Fatalf("anchor write: %q %v", kind, err)
	}

	// Viewers can comment.
	code, quiet := vicC.do("POST", "/revisions/"+revID+"/threads", map[string]any{"path": "docs/index.md", "anchor": map[string]any{"quote": "Handbook"}, "body": "Is this the right title?"})
	if code != 201 || quiet["state"] != "open" || len(quiet["comments"].([]any)) != 1 {
		t.Fatalf("create: %d %v", code, quiet)
	}
	code, busy := samC.do("POST", "/revisions/"+revID+"/threads", map[string]any{"path": "docs/index.md", "anchor": map[string]any{"quote": "Start here."}, "body": "Let's say more."})
	if code != 201 {
		t.Fatalf("create 2: %d", code)
	}
	if code, _ := samC.do("POST", "/revisions/"+revID+"/threads", map[string]any{"path": "docs/index.md", "body": "  "}); code != 422 {
		t.Fatalf("empty body: %d", code)
	}
	busyID, quietID := busy["id"].(string), quiet["id"].(string)
	// Activity on the second thread: a reply from someone else and a reaction.
	code, reply := vicC.do("POST", "/threads/"+busyID+"/comments", map[string]any{"body": "Agreed, add the setup link."})
	if code != 201 {
		t.Fatalf("reply: %d", code)
	}
	firstID := busy["comments"].([]any)[0].(map[string]any)["id"].(string)
	if code, _ := vicC.do("PUT", "/comments/"+firstID+"/reactions/+1", nil); code != 204 {
		t.Fatalf("react: %d", code)
	}
	// Clients encode the plus sign.
	if code, _ := vicC.do("PUT", "/comments/"+firstID+"/reactions/%2B1", nil); code != 204 {
		t.Fatalf("encoded reaction: %d", code)
	}
	if code, _ := vicC.do("PUT", "/comments/"+firstID+"/reactions/party", nil); code != 422 {
		t.Fatalf("unknown reaction: %d", code)
	}
	_, list := samC.do("GET", "/revisions/"+revID+"/threads", nil)
	items := list["items"].([]any)
	if items[0].(map[string]any)["id"] != busyID {
		t.Fatalf("hot sort should put the busy thread first: %v", toJSON(items))
	}
	if r := items[0].(map[string]any)["comments"].([]any)[0].(map[string]any)["reactions"]; toJSON(r) == "[]" {
		t.Fatalf("reactions missing: %v", r)
	}
	// Editing and deleting are for authors.
	replyID := reply["id"].(string)
	if code, _ := samC.do("PATCH", "/comments/"+replyID, map[string]any{"body": "hijack"}); code != 403 {
		t.Fatalf("edit someone else's comment: %d", code)
	}
	if code, _ := vicC.do("PATCH", "/comments/"+replyID, map[string]any{"body": "Agreed: add the setup link."}); code != 204 {
		t.Fatalf("edit own: %d", code)
	}
	// Resolving: contributors or the thread's author.
	if code, _ := vicC.do("PATCH", "/threads/"+busyID, map[string]any{"state": "resolved"}); code != 403 {
		t.Fatalf("viewer resolves someone else's thread: %d", code)
	}
	if code, th := samC.do("PATCH", "/threads/"+busyID, map[string]any{"state": "resolved"}); code != 200 || th["state"] != "resolved" {
		t.Fatalf("resolve: %d %v", code, th)
	}
	if code, _ := vicC.do("PATCH", "/threads/"+quietID, map[string]any{"state": "resolved"}); code != 200 {
		t.Fatalf("author resolves own thread: %d", code)
	}
	if code, th := samC.do("PATCH", "/threads/"+busyID, map[string]any{"state": "open"}); code != 200 || th["state"] != "open" {
		t.Fatalf("reopen: %d", code)
	}
	if code, _ := vicC.do("DELETE", "/comments/"+replyID, nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	_, th := samC.do("GET", "/threads/"+busyID, nil)
	if c := th["comments"].([]any)[1].(map[string]any); c["deleted"] != true || c["body"] != "" {
		t.Fatalf("deleted comment: %v", c)
	}
	// Filters: path; closed revisions take no new threads.
	if _, l := samC.do("GET", "/revisions/"+revID+"/threads?path=docs/other.md", nil); len(l["items"].([]any)) != 0 {
		t.Fatalf("path filter: %v", l)
	}
	samC.do("POST", "/revisions/"+revID+"/close", nil)
	if code, _ := samC.do("POST", "/revisions/"+revID+"/threads", map[string]any{"path": "docs/index.md", "body": "late"}); code != 409 {
		t.Fatalf("thread on a closed revision: %d", code)
	}
}
