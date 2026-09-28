package app

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/kmdn-app/kmdn/internal/config"
	"github.com/kmdn-app/kmdn/internal/mail"
	"github.com/kmdn-app/kmdn/internal/orgs"
	"github.com/kmdn-app/kmdn/internal/policy"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/store"
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
	a.Policy.UpgradeURL = func(_ context.Context, orgID string) string { return "/billing/" + orgID }
	alice, _ := users.Create(ctx, a.DB, "alice@acme.dev", "Alice", false)
	signIn(t, a, root, alice)
	root.do("POST", "/orgs", map[string]any{"name": "Acme", "slug": "acme"})
	if code, b := root.do("POST", "/orgs/acme/admin/invites", map[string]any{"email": "bob@acme.dev"}); code != 201 {
		t.Fatalf("second seat: %d %v", code, b)
	}
	code, b := root.do("POST", "/orgs/acme/admin/invites", map[string]any{"email": "carl@acme.dev"})
	if code != 409 || b["code"] != "limit_reached" || !strings.HasPrefix(b["params"].(map[string]any)["upgrade_url"].(string), "/billing/org_") {
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

// A repository over its org's file or size cap stops syncing, with a
// message; one over the size cap loses its mirror.
func TestRepoCaps(t *testing.T) {
	a, root := newApp(t, multiOrgs)
	ctx := context.Background()
	olga, _ := users.Create(ctx, a.DB, "olga@acme.dev", "Olga", false)
	signIn(t, a, root, olga)
	root.do("POST", "/orgs", map[string]any{"name": "Acme", "slug": "acme"})
	var big strings.Builder
	for i := 0; big.Len() < 3<<20; i++ {
		fmt.Fprintf(&big, "%x ", sha256.Sum256([]byte(strconv.Itoa(i))))
	}
	repoID, _ := connectLocalIn(t, a, root, "acme", map[string]string{"docs/index.md": "# Acme\n", "docs/a.md": "# A\n", "docs/big.md": big.String()})
	runJobs(t, a) // indexing reads every page, so the mirror holds big.md
	repo, _ := repos.Get(ctx, a.DB, repoID)
	health := func() repos.Repo {
		r, _ := repos.Get(ctx, a.DB, repoID)
		return r
	}
	a.Policy.Limits = func(context.Context, string) policy.Limits { return policy.Limits{RepoFiles: 2} }
	if err := a.Repos.Sync(ctx, repoID); err != nil {
		t.Fatal(err)
	}
	if r := health(); r.Health != repos.HealthDegraded || !strings.Contains(r.HealthDetail, "3 files") {
		t.Fatalf("file cap: %s %q", r.Health, r.HealthDetail)
	}
	a.Policy.Limits = func(context.Context, string) policy.Limits { return policy.Limits{RepoMB: 1} }
	_ = a.Repos.Sync(ctx, repoID)
	if r := health(); r.Health != repos.HealthDegraded || !strings.Contains(r.HealthDetail, "1 MB") {
		t.Fatalf("size cap: %s %q", r.Health, r.HealthDetail)
	}
	if _, err := os.Stat(a.Repos.Mirror(repo).Path); !os.IsNotExist(err) {
		t.Fatalf("mirror kept: %v", err)
	}
	a.Policy.Limits = nil
	if err := a.Repos.Sync(ctx, repoID); err != nil || health().Health != repos.HealthOK {
		t.Fatalf("under the caps again: %v %s", err, health().Health)
	}
}

// A forge an org added answers with the clone URL it likes: it's checked
// like a typed one, and must be on the forge's own host.
func TestOrgForgeCloneURL(t *testing.T) {
	a, root := newApp(t, func(c *config.Config) {
		multiOrgs(c)
		c.Policy.Strict = true
		c.Server.ContentBaseURL = "http://content.localhost"
	})
	ctx := context.Background()
	alice, _ := users.Create(ctx, a.DB, "alice@acme.dev", "Alice", false)
	signIn(t, a, root, alice)
	_, org := root.do("POST", "/orgs", map[string]any{"name": "Acme", "slug": "acme"})
	var clone string
	gl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":7,"path":"docs","path_with_namespace":"acme/docs","default_branch":"main","visibility":"private","http_url_to_repo":"` + clone + `"}`))
	}))
	defer gl.Close()
	a.Repos.Adapters.OrgHTTP = gl.Client() // the fake is local; the guard is tested elsewhere
	if _, err := store.Exec(ctx, a.DB, `INSERT INTO forge_hosts (id, kind, base_url, api_url, display_name, org_id, created_at) VALUES ('fh_org', 'gitlab', ?, ?, 'GitLab', ?, 0)`, gl.URL, gl.URL+"/api/v4", org["id"]); err != nil {
		t.Fatal(err)
	}
	for u, want := range map[string]string{
		"https://gitlab.com/acme/docs.git":   "another host",
		gl.URL + "/acme/docs.git":            "won't use",
		"https://169.254.169.254/x/docs.git": "won't use",
	} {
		clone = u
		code, b := root.do("POST", "/orgs/acme/repos", map[string]any{"forge_host_id": "fh_org", "owner": "acme", "name": "docs", "token": "glpat-x"})
		if code != 422 || !strings.Contains(toJSON(b), want) {
			t.Errorf("clone URL %s: %d %v", u, code, b)
		}
	}
}
