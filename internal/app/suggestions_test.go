package app

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

func TestSuggestions(t *testing.T) {
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
	sam, samC := mk("sam@northwind.dev", "Sam", access.Contributor)
	tom, tomC := mk("tom@northwind.dev", "Tom", access.Contributor)
	a.Collab.Options.QuietPeriod = 0
	_, rev := samC.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Start", "path": "docs/index.md"})
	revID := rev["id"].(string)
	rv, _ := revisions.Get(ctx, a.DB, revID)
	repo, _ := repos.Get(ctx, a.DB, repoID)
	samCaller := revisions.Caller{User: sam, Role: access.Contributor}
	if err := a.Collab.Apply(ctx, repo, rv, samCaller, "docs/index.md", "# Handbook\n\nStart here.\n\nMore.\n", "human"); err != nil {
		t.Fatal(err)
	}

	// Sam suggests " now" and Tom's suggestion deletes "More.".
	ins := func(id, author string) string {
		return fmt.Sprintf(`{"type":"insertion","attrs":{"id":%q,"author":%q}}`, id, author)
	}
	del := func(id, author string) string {
		return fmt.Sprintf(`{"type":"deletion","attrs":{"id":%q,"author":%q}}`, id, author)
	}
	doc := fmt.Sprintf(`{"type":"doc","content":[
		{"type":"heading","attrs":{"level":1},"content":[{"type":"text","text":"Handbook"}]},
		{"type":"paragraph","content":[{"type":"text","text":"Start here"},{"type":"text","text":" now","marks":[%s]},{"type":"text","text":"."}]},
		{"type":"paragraph","content":[{"type":"text","text":"More.","marks":[%s]}]}]}`, ins("s1", sam.ID), del("s2", tom.ID))
	if err := a.Collab.ApplyDoc(ctx, repo, rv, samCaller, "docs/index.md", json.RawMessage(doc), "human"); err != nil {
		t.Fatal(err)
	}
	a.Collab.FlushRevision(ctx, revID)
	content := func() string {
		var md string
		if err := store.QueryRow(ctx, a.DB, `SELECT content_md FROM revision_files WHERE revision_id = ? AND path = 'docs/index.md'`, revID).Scan(&md); err != nil {
			t.Fatal(err)
		}
		return md
	}
	// Pending suggestions aren't in the page.
	if got := content(); got != "# Handbook\n\nStart here.\n\nMore.\n" {
		t.Fatalf("materialized with pending suggestions: %q", got)
	}
	code, list := samC.do("GET", "/revisions/"+revID+"/suggestions", nil)
	items, _ := list["items"].([]any)
	if code != 200 || len(items) != 2 || items[0].(map[string]any)["inserted"] != " now" || items[1].(map[string]any)["path"] != "docs/index.md" {
		t.Fatalf("list: %d %v", code, list)
	}

	// Tom isn't an editor of the revision: he can only resolve his own.
	if code, _ := tomC.do("POST", "/revisions/"+revID+"/suggestions/resolve", map[string]any{"path": "docs/index.md", "action": "accept", "ids": []string{"s1"}}); code != 403 {
		t.Fatalf("resolve someone else's: %d", code)
	}
	if code, r := tomC.do("POST", "/revisions/"+revID+"/suggestions/resolve", map[string]any{"path": "docs/index.md", "action": "reject", "author": tom.ID}); code != 200 || r["resolved"] != float64(1) {
		t.Fatalf("reject own: %d %v", code, r)
	}
	if code, _ := samC.do("POST", "/revisions/"+revID+"/suggestions/resolve", map[string]any{"path": "docs/index.md", "action": "maybe"}); code != 422 {
		t.Fatalf("bad action: %d", code)
	}
	if code, r := samC.do("POST", "/revisions/"+revID+"/suggestions/resolve", map[string]any{"path": "docs/index.md", "action": "accept", "ids": []string{"s1"}}); code != 200 || r["resolved"] != float64(1) {
		t.Fatalf("accept: %d %v", code, r)
	}
	a.Collab.FlushRevision(ctx, revID)
	if got := content(); got != "# Handbook\n\nStart here now.\n\nMore.\n" {
		t.Fatalf("after accept: %q", got)
	}
	if _, list := samC.do("GET", "/revisions/"+revID+"/suggestions?path=docs/index.md", nil); len(list["items"].([]any)) != 0 {
		t.Fatalf("still pending: %v", list)
	}
	// The accepted text is still Sam's (for Co-authored-by).
	_, state, _ := a.Collab.StateOf(ctx, revID, "docs/index.md")
	counts, _ := a.Engine.YContributions(ctx, state)
	byKind := map[string]int{}
	for client, n := range counts {
		var uid string
		_ = store.QueryRow(ctx, a.DB, `SELECT c.user_id FROM ydoc_clients c JOIN ydocs d ON d.id = c.ydoc_id WHERE d.revision_id = ? AND c.client_id = ?`, revID, int64(client)).Scan(&uid)
		byKind[uid] += n
	}
	if byKind[tom.ID] != 0 {
		t.Fatalf("tom credited for a rejected suggestion: %v", byKind)
	}
	var events int
	_ = store.QueryRow(ctx, a.DB, `SELECT COUNT(*) FROM revision_events WHERE revision_id = ? AND kind IN ('suggestions_accepted', 'suggestions_rejected')`, revID).Scan(&events)
	if events != 2 {
		t.Fatalf("events: %d", events)
	}
}
