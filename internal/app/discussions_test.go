package app

import (
	"context"
	"strings"
	"testing"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/threads"
	"github.com/kmdn-app/kmdn/internal/users"
)

func TestDiscussions(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	repoID, remote := connectLocalRemote(t, a, admin, map[string]string{"docs/policy.md": "# Remote work\n\nWork from anywhere **two days** a week.\n\nAsk your lead first.\n"})
	mk := func(email, name string, role access.Role) (users.User, *tc) {
		u, _ := users.Create(ctx, a.DB, email, name, false)
		_ = access.Grant(ctx, a.DB, repoID, "user", u.ID, role)
		c := &tc{t: t, base: admin.base, c: newClient()}
		signIn(t, a, c, u)
		return u, c
	}
	_, luisC := mk("luis@northwind.dev", "Luis", access.Viewer)
	_, samC := mk("sam@northwind.dev", "Sam", access.Contributor)

	// A viewer comments on the published page.
	code, d := luisC.do("POST", "/repos/"+repoID+"/discussions", map[string]any{"path": "docs/policy.md", "anchor": map[string]any{"quote": "two days a week", "prefix": "Work from anywhere ", "suffix": "."}, "body": "This is outdated since July."})
	if code != 201 || d["kind"] != "discussion" || d["repo_id"] != repoID {
		t.Fatalf("create: %d %v", code, d)
	}
	discID := d["id"].(string)
	if sha := d["anchor"].(map[string]any)["sha"]; sha == "" || sha == nil {
		t.Fatalf("anchor sha: %v", d["anchor"])
	}
	_, other := luisC.do("POST", "/repos/"+repoID+"/discussions", map[string]any{"path": "docs/policy.md", "anchor": map[string]any{"quote": "Ask your lead first."}, "body": "Which lead?"})
	if code, _ := luisC.do("POST", "/repos/"+repoID+"/discussions", map[string]any{"path": "docs/nope.txt", "anchor": map[string]any{"quote": "x"}, "body": "?"}); code != 422 {
		t.Fatalf("non-page: %d", code)
	}
	if _, list := samC.do("GET", "/repos/"+repoID+"/discussions?path=docs/policy.md", nil); len(list["items"].([]any)) != 2 {
		t.Fatalf("list: %v", list)
	}

	// Viewers can't start a fix; contributors can, once.
	if code, _ := luisC.do("POST", "/threads/"+discID+"/fix-this", nil); code != 403 {
		t.Fatalf("viewer fix: %d", code)
	}
	code, fx := samC.do("POST", "/threads/"+discID+"/fix-this", nil)
	if code != 201 {
		t.Fatalf("fix: %d %v", code, fx)
	}
	rev := fx["revision"].(map[string]any)
	if rev["title"] != "Fix: Remote work" || !strings.Contains(rev["description"].(string), "> two days a week") || !strings.Contains(rev["description"].(string), "outdated since July") {
		t.Fatalf("fix revision: %v", rev)
	}
	if code, _ := samC.do("POST", "/threads/"+discID+"/fix-this", nil); code != 409 {
		t.Fatalf("second fix: %d", code)
	}
	if _, th := samC.do("GET", "/threads/"+discID, nil); th["fix_revision"].(map[string]any)["number"] != rev["number"] {
		t.Fatalf("linked: %v", th["fix_revision"])
	}

	// Published rewrites the sentence: that discussion is outdated, the other moves on.
	writeFile(t, remote, "docs/policy.md", "# Remote work\n\nWork from anywhere three days a week.\n\nAsk your lead first.\n")
	gitIn(t, remote, "commit", "--quiet", "-am", "Three days")
	if err := a.Repos.Sync(ctx, repoID); err != nil {
		t.Fatal(err)
	}
	head := gitIn(t, remote, "rev-parse", "HEAD")
	if _, th := samC.do("GET", "/threads/"+discID, nil); th["outdated"] != true {
		t.Fatalf("not outdated: %v", th)
	}
	if _, th := samC.do("GET", "/threads/"+other["id"].(string), nil); th["outdated"] == true || th["anchor"].(map[string]any)["sha"] != head {
		t.Fatalf("other: %v", th)
	}

	// Publishing the fix resolves the discussion.
	rv, _ := revisions.Get(ctx, a.DB, rev["id"].(string))
	a.Revisions.Changed(ctx, rv, "published")
	if th, _ := threads.Get(ctx, a.DB, discID); th.State != threads.StateResolved {
		t.Fatalf("not resolved: %s", th.State)
	}
	if th, _ := threads.Get(ctx, a.DB, other["id"].(string)); th.State != threads.StateOpen {
		t.Fatal("unrelated discussion resolved")
	}
}

func TestQuoteFound(t *testing.T) {
	text := "Work from anywhere two days a week.\nAsk your lead first."
	for q, want := range map[string]bool{"two days a week": true, "Two  Days\na week": true, "three days": false, "": true} {
		if got := threads.Found(text, threads.Anchor{Quote: q}); got != want {
			t.Errorf("%q: %v", q, got)
		}
	}
}
