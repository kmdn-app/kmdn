package app

import (
	"context"
	"testing"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/users"
)

func TestFollowsAndReads(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	repoID, remote := connectLocalRemote(t, a, admin, map[string]string{"docs/policy.md": "# Remote work\n\nTwo days.\n", "docs/other.md": "# Other\n"})
	mk := func(email, name string, role access.Role) (users.User, *tc) {
		u, _ := users.Create(ctx, a.DB, email, name, false)
		_ = access.Grant(ctx, a.DB, repoID, "user", u.ID, role)
		c := &tc{t: t, base: admin.base, c: newClient()}
		signIn(t, a, c, u)
		return u, c
	}
	_, luisC := mk("luis@northwind.dev", "Luis", access.Viewer)
	_, priyaC := mk("priya@northwind.dev", "Priya", access.Viewer)
	sam, samC := mk("sam@northwind.dev", "Sam", access.Contributor)
	tom, tomC := mk("tom@northwind.dev", "Tom", access.Maintainer)
	a.Collab.Options.QuietPeriod = 0
	head := gitIn(t, remote, "rev-parse", "HEAD")

	// Luis follows the page and reads it; Priya follows the folder.
	if code, st := luisC.do("PUT", "/repos/"+repoID+"/follows?path=docs/policy.md", nil); code != 200 || st["following"] != true || st["page"] != true {
		t.Fatalf("follow: %d %v", code, st)
	}
	if code, _ := luisC.do("PUT", "/repos/"+repoID+"/follows?path=docs/notes.txt", nil); code != 422 {
		t.Fatalf("follow non-page: %d", code)
	}
	if code, st := priyaC.do("PUT", "/repos/"+repoID+"/follows?path=docs&folder=true", nil); code != 200 || st["following"] != true {
		t.Fatalf("follow folder: %d %v", code, st)
	}
	if _, st := priyaC.do("GET", "/repos/"+repoID+"/follows?path=docs/policy.md", nil); st["following"] != true || st["folder"] != "docs" {
		t.Fatalf("via folder: %v", st)
	}
	if _, r := luisC.do("POST", "/repos/"+repoID+"/reads", map[string]any{"path": "docs/policy.md", "sha": head}); r["previous"] != nil {
		t.Fatalf("first read: %v", r)
	}
	if _, r := luisC.do("POST", "/repos/"+repoID+"/reads", map[string]any{"path": "docs/policy.md", "sha": head}); r["previous"].(map[string]any)["sha"] != head {
		t.Fatalf("second read: %v", r)
	}

	// Sam's revision changes the page and gets published.
	_, rev := samC.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Three days", "path": "docs/policy.md"})
	revID := rev["id"].(string)
	rv, _ := revisions.Get(ctx, a.DB, revID)
	repo, _ := repos.Get(ctx, a.DB, repoID)
	if err := a.Collab.Apply(ctx, repo, rv, revisions.Caller{User: sam, Role: access.Contributor}, "docs/policy.md", "# Remote work\n\nThree days.\n", "human"); err != nil {
		t.Fatal(err)
	}
	a.Collab.FlushRevision(ctx, revID)
	samC.do("POST", "/revisions/"+revID+"/submit", map[string]any{"reviewers": []string{tom.ID}})
	tomC.do("POST", "/revisions/"+revID+"/approve", nil)
	if code, body := tomC.do("POST", "/revisions/"+revID+"/publish", map[string]any{}); code != 202 {
		t.Fatalf("publish: %d %v", code, body)
	}
	runJobs(t, a)
	if err := a.Notify.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]*tc{"luis": luisC, "priya": priyaC} {
		_, inbox := c.do("GET", "/notifications", nil)
		items := inbox["items"].([]any)
		if len(items) != 1 || items[0].(map[string]any)["kind"] != "page_published" || items[0].(map[string]any)["data"].(map[string]any)["path"] != "docs/policy.md" {
			t.Fatalf("%s's inbox: %v", name, items)
		}
	}
	// Sam, an editor, now follows the page for a while.
	if _, st := samC.do("GET", "/repos/"+repoID+"/follows?path=docs/policy.md", nil); st["following"] != true || st["auto_until"] == nil {
		t.Fatalf("auto follow: %v", st)
	}

	// Luis's followed page changed since he read it.
	_, up := luisC.do("GET", "/repos/"+repoID+"/follows/updates", nil)
	items := up["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["path"] != "docs/policy.md" || items[0].(map[string]any)["read_sha"] != head {
		t.Fatalf("updates: %v", up)
	}
	newHead := items[0].(map[string]any)["sha"].(string)
	luisC.do("POST", "/repos/"+repoID+"/reads", map[string]any{"path": "docs/policy.md", "sha": newHead})
	if _, up := luisC.do("GET", "/repos/"+repoID+"/follows/updates", nil); len(up["items"].([]any)) != 0 {
		t.Fatalf("still updated after reading: %v", up)
	}
	if _, st := luisC.do("DELETE", "/repos/"+repoID+"/follows?path=docs/policy.md", nil); st["following"] != false {
		t.Fatalf("unfollow: %v", st)
	}
}
