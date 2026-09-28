package app

import (
	"context"
	"strings"
	"testing"

	"github.com/kmdn-app/kmdn/internal/config"
	"github.com/kmdn-app/kmdn/internal/mail"
	"github.com/kmdn-app/kmdn/internal/orgs"
	"github.com/kmdn-app/kmdn/internal/policy"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/users"
)

// Strict mode refuses clone URLs that reach the server itself or private
// networks, and org forges when the instance provides them.
func TestStrictPolicy(t *testing.T) {
	a, root := newApp(t, func(c *config.Config) {
		multiOrgs(c)
		c.Policy.Strict, c.Policy.NoOrgForges = true, true
		c.Server.ContentBaseURL = "http://content.localhost"
	})
	ctx := context.Background()
	alice, _ := users.Create(ctx, a.DB, "alice@acme.dev", "Alice", false)
	signIn(t, a, root, alice)
	root.do("POST", "/orgs", map[string]any{"name": "Acme", "slug": "acme"})
	host, _ := repos.EnsureGitHost(ctx, a.DB)
	for _, u := range []string{"file:///etc", a.Config.DataDir, "ext::sh -c id", "https://127.0.0.1/x.git", "git@localhost:x.git"} {
		if code, b := root.do("POST", "/orgs/acme/repos", map[string]any{"forge_host_id": host.ID, "clone_url": u}); code != 422 || !strings.Contains(toJSON(b), "clone_url") {
			t.Errorf("clone %q: %d %v", u, code, b)
		}
	}
	if code, _ := root.do("POST", "/orgs/acme/admin/forges", map[string]any{"kind": "gitlab", "base_url": "https://gitlab.example.com"}); code != 403 {
		t.Fatalf("org forge with no_org_forges: %d", code)
	}
	if a.Repos.Git.AllowProtocols != "https:ssh" {
		t.Fatalf("git protocols: %q", a.Repos.Git.AllowProtocols)
	}
}

// Org limits: members (pending invites count) and repositories.
func TestOrgLimits(t *testing.T) {
	a, root := newApp(t, multiOrgs)
	ctx := context.Background()
	a.Invites.Mail = &mail.Capture{}
	a.Policy.Limits = func(context.Context, string) policy.Limits { return policy.Limits{Members: 2, Repos: 1} }
	alice, _ := users.Create(ctx, a.DB, "alice@acme.dev", "Alice", false)
	signIn(t, a, root, alice)
	root.do("POST", "/orgs", map[string]any{"name": "Acme", "slug": "acme"})
	if code, b := root.do("POST", "/orgs/acme/admin/invites", map[string]any{"email": "bob@acme.dev"}); code != 201 {
		t.Fatalf("second seat: %d %v", code, b)
	}
	code, b := root.do("POST", "/orgs/acme/admin/invites", map[string]any{"email": "carl@acme.dev"})
	if code != 409 || b["code"] != "limit_reached" {
		t.Fatalf("third seat: %d %v", code, b)
	}
	connectLocalIn(t, a, root, "acme", map[string]string{"docs/index.md": "# One\n"})
	host, _ := repos.EnsureGitHost(ctx, a.DB)
	if code, b := root.do("POST", "/orgs/acme/repos", map[string]any{"forge_host_id": host.ID, "clone_url": "file:///nope"}); code != 409 || b["code"] != "limit_reached" {
		t.Fatalf("second repo: %d %v", code, b)
	}
}

// A suspended org is read-only: members read and comment, nobody edits.
func TestSuspendedOrgIsReadOnly(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	tom, _ := users.Create(ctx, a.DB, "tom@northwind.dev", "Tom", false)
	signIn(t, a, admin, maya)
	tomC := &tc{t: t, base: admin.base, c: newClient()}
	signIn(t, a, tomC, tom)
	repoID := connectLocal(t, a, admin, map[string]string{"docs/index.md": "# Handbook\n"})
	admin.do("PUT", "/repos/"+repoID+"/members/user/"+tom.ID, map[string]any{"role": "maintainer"})
	if code, _ := admin.do("PATCH", "/admin/orgs/default", map[string]any{"status": "suspended", "reason": "The subscription ended."}); code != 200 {
		t.Fatalf("suspend: %d", code)
	}
	if code, r := tomC.do("GET", "/repos/"+repoID, nil); code != 200 || r["role"] != "viewer" {
		t.Fatalf("tom in a suspended org: %d %v", code, r)
	}
	if code, _ := tomC.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Edit", "path": "docs/index.md"}); code != 403 {
		t.Fatalf("revision in a suspended org: %d", code)
	}
	if _, o := tomC.do("GET", "/orgs/default", nil); o["status"] != "suspended" || o["status_reason"] != "The subscription ended." {
		t.Fatalf("org: %v", o)
	}
	if err := orgs.SetStatus(ctx, a.DB, orgs.DefaultID, orgs.Active, "ignored"); err != nil {
		t.Fatal(err)
	}
	if _, r := tomC.do("GET", "/repos/"+repoID, nil); r["role"] != "maintainer" {
		t.Fatalf("after resuming: %v", r)
	}
}
