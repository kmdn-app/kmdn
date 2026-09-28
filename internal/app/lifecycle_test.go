package app

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kmdn-app/kmdn/internal/lifecycle"
	"github.com/kmdn-app/kmdn/internal/orgs"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

// An owner deletes the org: it's gone for everyone at once, an instance
// admin can restore it within the grace period, then it's purged with its
// repositories, uploads and key.
func TestDeleteOrg(t *testing.T) {
	a, root := newApp(t, multiOrgs)
	ctx := context.Background()
	olga, _ := users.Create(ctx, a.DB, "olga@acme.dev", "Olga", false)
	pat, _ := users.Create(ctx, a.DB, "pat@acme.dev", "Pat", false)
	admin, _ := users.Create(ctx, a.DB, "ana@kmdn.dev", "Ana", true)
	signIn(t, a, root, olga)
	_, org := root.do("POST", "/orgs", map[string]any{"name": "Acme", "slug": "acme"})
	orgID := org["id"].(string)
	repoID, _ := connectLocalIn(t, a, root, "acme", map[string]string{"docs/index.md": "# Acme\n", "docs/images/a.png": "x"})
	_ = orgs.AddMember(ctx, a.DB, orgID, pat.ID, orgs.Member, "")
	patC := &tc{t: t, base: root.base, c: newClient()}
	signIn(t, a, patC, pat)
	adminC := &tc{t: t, base: root.base, c: newClient()}
	signIn(t, a, adminC, admin)
	// Something to purge: an upload, a secret, the mirror.
	_, rev := root.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Photos"})
	if code, up := root.upload("/revisions/"+rev["id"].(string)+"/assets", "docs/index.md", "desk.png", pngBytes(t)); code != 201 {
		t.Fatalf("upload: %d %v", code, up)
	}
	if _, err := a.Secrets.PutOrg(ctx, a.DB, orgID, "test", []byte("s3cret")); err != nil {
		t.Fatal(err)
	}
	repo, _ := repos.Get(ctx, a.DB, repoID)
	mirror := a.Repos.Mirror(repo).Path
	if _, err := os.Stat(mirror); err != nil {
		t.Fatalf("mirror: %v", err)
	}

	if code, _ := patC.do("DELETE", "/orgs/acme", map[string]any{"confirm": "acme"}); code != 403 {
		t.Fatalf("member deletes: %d", code)
	}
	if code, b := root.do("DELETE", "/orgs/acme", map[string]any{"confirm": "acm"}); code != 422 {
		t.Fatalf("wrong confirmation: %d %v", code, b)
	}
	if code, b := root.do("DELETE", "/orgs/acme", map[string]any{"confirm": "acme"}); code != 200 || b["purge_after"] == nil {
		t.Fatalf("delete: %d %v", code, b)
	}
	for _, c := range []*tc{root, patC} {
		if code, _ := c.do("GET", "/orgs/acme", nil); code != 404 {
			t.Fatalf("org after deletion: %d", code)
		}
		if code, _ := c.do("GET", "/repos/"+repoID, nil); code != 404 {
			t.Fatalf("repo after deletion: %d", code)
		}
	}
	if _, list := root.do("GET", "/orgs", nil); len(list["items"].([]any)) != 0 {
		t.Fatalf("org still listed: %v", list)
	}
	if _, list := adminC.do("GET", "/admin/orgs/deleted", nil); !strings.Contains(toJSON(list), `"slug":"acme"`) {
		t.Fatalf("deleted orgs: %v", list)
	}
	if code, _ := adminC.do("POST", "/admin/orgs/acme/restore", nil); code != 200 {
		t.Fatalf("restore: %d", code)
	}
	if code, _ := root.do("GET", "/repos/"+repoID, nil); code != 200 {
		t.Fatalf("repo after restore: %d", code)
	}
	root.do("DELETE", "/orgs/acme", map[string]any{"confirm": "acme"})
	a.Lifecycle.Grace = time.Hour
	if n, err := a.Lifecycle.PurgeDue(ctx); err != nil || n != 0 {
		t.Fatalf("purged inside the grace period: %d %v", n, err)
	}
	a.Lifecycle.Grace = time.Nanosecond
	if n, err := a.Lifecycle.PurgeDue(ctx); err != nil || n != 1 {
		t.Fatalf("purge: %d %v", n, err)
	}
	for table, q := range map[string]string{
		"orgs": `SELECT COUNT(*) FROM orgs WHERE id = ?`, "repos": `SELECT COUNT(*) FROM repos WHERE org_id = ?`, "revisions": `SELECT COUNT(*) FROM revisions WHERE repo_id = '` + repoID + `' AND ? <> ''`,
		"uploads": `SELECT COUNT(*) FROM uploads WHERE org_id = ?`, "secrets": `SELECT COUNT(*) FROM secrets WHERE org_id = ?`, "org_keys": `SELECT COUNT(*) FROM org_keys WHERE org_id = ?`,
		"org_members": `SELECT COUNT(*) FROM org_members WHERE org_id = ?`,
	} {
		var n int
		if err := store.QueryRow(ctx, a.DB, q, orgID).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s left after purge: %d %v", table, n, err)
		}
	}
	if _, err := os.Stat(mirror); !os.IsNotExist(err) {
		t.Errorf("mirror left: %v", err)
	}
	entries, _ := os.ReadDir(a.Config.DataDir + "/uploads/" + orgID)
	for _, e := range entries {
		sub, _ := os.ReadDir(a.Config.DataDir + "/uploads/" + orgID + "/" + e.Name())
		if len(sub) > 0 {
			t.Errorf("upload left: %s", e.Name())
		}
	}
}

// People erase their account: sign-ins and memberships go, what they wrote
// stays as "Deleted user"; an org can't be left without an owner, and orgs
// where they were alone are deleted.
func TestEraseAccount(t *testing.T) {
	a, root := newApp(t, multiOrgs)
	ctx := context.Background()
	olga, _ := users.Create(ctx, a.DB, "olga@acme.dev", "Olga", false)
	pat, _ := users.Create(ctx, a.DB, "pat@acme.dev", "Pat", false)
	signIn(t, a, root, olga)
	_, org := root.do("POST", "/orgs", map[string]any{"name": "Acme", "slug": "acme"})
	orgID := org["id"].(string)
	repoID, _ := connectLocalIn(t, a, root, "acme", map[string]string{"docs/index.md": "# Acme\n"})
	_ = orgs.AddMember(ctx, a.DB, orgID, pat.ID, orgs.Member, "")
	root.do("PUT", "/repos/"+repoID+"/members/user/"+pat.ID, map[string]any{"role": "contributor"})
	patC := &tc{t: t, base: root.base, c: newClient()}
	signIn(t, a, patC, pat)
	_, solo := patC.do("POST", "/orgs", map[string]any{"name": "Pat's", "slug": "pats"})
	_, rev := patC.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Edit"})
	if code, b := patC.do("POST", "/revisions/"+rev["id"].(string)+"/threads", map[string]any{"path": "docs/index.md", "body": "A note from Pat."}); code != 201 {
		t.Fatalf("thread: %d %v", code, b)
	}

	// Olga is Acme's only owner, and it has Pat: not yet.
	if code, b := root.do("DELETE", "/me", map[string]any{"confirm": "olga@acme.dev"}); code != 409 || b["code"] != "sole_owner" {
		t.Fatalf("sole owner: %d %v", code, b)
	}
	if code, _ := patC.do("DELETE", "/me", map[string]any{"confirm": "someone@else.dev"}); code != 422 {
		t.Fatalf("wrong confirmation: %d", code)
	}
	if code, b := patC.do("DELETE", "/me", map[string]any{"confirm": "Pat@acme.dev"}); code != 200 {
		t.Fatalf("erase: %d %v", code, b)
	}
	if code, _ := patC.do("GET", "/me", nil); code != 401 {
		t.Fatalf("still signed in: %d", code)
	}
	u, _ := users.ByID(ctx, a.DB, pat.ID)
	if u.Name != lifecycle.ErasedName || u.Status != users.Deactivated || strings.Contains(u.Email, "pat@") {
		t.Fatalf("account: %+v", u)
	}
	if role, _ := orgs.MemberRole(ctx, a.DB, orgID, u); role != "" {
		t.Fatalf("still a member: %q", role)
	}
	if o, _ := orgs.ByID(ctx, a.DB, solo["id"].(string)); o.Status != orgs.Deleting {
		t.Fatalf("pat's own org: %+v", o)
	}
	// Pat's words stay, attributed to the deleted user.
	_, threads := root.do("GET", "/revisions/"+rev["id"].(string)+"/threads", nil)
	if s := toJSON(threads); !strings.Contains(s, "A note from Pat.") || !strings.Contains(s, lifecycle.ErasedName) {
		t.Fatalf("threads: %s", s)
	}
	// The address is free for a new account.
	if _, err := users.Create(ctx, a.DB, "pat@acme.dev", "Pat again", false); err != nil {
		t.Fatalf("new account with the address: %v", err)
	}
}
