package app

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/kmdn-app/kmdn/internal/orgs"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

// fakeGitHub answers what the connect flow asks GitHub: the user token for a
// code, the installations that person can access, the App's installations.
func fakeGitHub(t *testing.T, access map[string][]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		fmt.Fprintf(w, `{"access_token":"tok-%s"}`, r.Form.Get("code"))
	})
	mux.HandleFunc("GET /user/installations", func(w http.ResponseWriter, r *http.Request) {
		code := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer tok-")
		var items []string
		for _, id := range access[code] {
			items = append(items, fmt.Sprintf(`{"id":%s,"account":{"login":"acct-%s"}}`, id, id))
		}
		fmt.Fprintf(w, `{"installations":[%s]}`, strings.Join(items, ","))
	})
	mux.HandleFunc("GET /app/installations", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"id":42,"account":{"login":"acct-42"}},{"id":43,"account":{"login":"acct-43"}}]`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func sharedGitHubHost(t *testing.T, a *App, gh *httptest.Server) repos.HostRecord {
	t.Helper()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	var h repos.HostRecord
	err := a.DB.InTx(context.Background(), func(tx *store.Tx) error {
		var err error
		h, err = repos.CreateHost(context.Background(), tx, a.Secrets, repos.HostInput{Kind: "github", BaseURL: gh.URL, APIURL: gh.URL, DisplayName: "GitHub",
			AppID: "7", AppSlug: "kmdn", ClientID: "Iv1.x", ClientSecret: "shh", PrivateKeyPEM: string(pemKey), WebhookSecret: "hook"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// step makes one browser request without following redirects and returns
// the redirect target.
func step(t *testing.T, c *tc, target string) *url.URL {
	t.Helper()
	nc := &http.Client{Jar: c.c.Jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if strings.HasPrefix(target, "/") {
		target = c.base + target
	}
	res, err := nc.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("GET %s: %d", target, res.StatusCode)
	}
	u, err := url.Parse(res.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// connectInstall runs the connect flow as c for installation inst, with
// GitHub answering for code; it returns where the flow ends.
func connectInstall(t *testing.T, c *tc, org, hostID, inst, code string) *url.URL {
	t.Helper()
	gh := step(t, c, "/api/v1/orgs/"+org+"/admin/forges/"+hostID+"/connect?installation="+inst)
	if !strings.HasSuffix(gh.Path, "/login/oauth/authorize") {
		t.Fatalf("expected GitHub authorize, got %s", gh)
	}
	return step(t, c, "/api/v1/auth/oauth/"+hostID+"/callback?code="+code+"&state="+url.QueryEscape(gh.Query().Get("state")))
}

func TestGitHubInstallationsPerOrg(t *testing.T) {
	a, root := newApp(t, multiOrgs)
	ctx := context.Background()
	alice, _ := users.Create(ctx, a.DB, "alice@acme.dev", "Alice", false)
	bob, _ := users.Create(ctx, a.DB, "bob@globex.dev", "Bob", false)
	signIn(t, a, root, alice)
	bobC := &tc{t: t, base: root.base, c: newClient()}
	signIn(t, a, bobC, bob)
	root.do("POST", "/orgs", map[string]any{"name": "Acme", "slug": "acme"})
	bobC.do("POST", "/orgs", map[string]any{"name": "Globex", "slug": "globex"})
	gh := fakeGitHub(t, map[string][]string{"alice": {"42"}, "bob": {"43"}, "bob-also-42": {"42", "43"}})
	h := sharedGitHubHost(t, a, gh)

	// The shared host shows in every org, and only instance admins remove it.
	if _, l := bobC.do("GET", "/orgs/globex/admin/forges", nil); !strings.Contains(toJSON(l), h.ID) {
		t.Fatalf("shared host missing: %v", l)
	}
	if code, _ := bobC.do("DELETE", "/orgs/globex/admin/forges/"+h.ID, nil); code != 403 {
		t.Fatalf("org removes a shared host: %d", code)
	}

	// Installing from the org: GitHub gets kmdn's state and hands it back.
	inst := step(t, root, "/api/v1/orgs/acme/admin/forges/"+h.ID+"/connect")
	if inst.Path != "/apps/kmdn/installations/new" || inst.Query().Get("state") == "" {
		t.Fatalf("install redirect: %s", inst)
	}
	authz := step(t, root, "/api/v1/admin/forges/github/setup?installation_id=42&setup_action=install&state="+url.QueryEscape(inst.Query().Get("state")))
	end := step(t, root, "/api/v1/auth/oauth/"+h.ID+"/callback?code=alice&state="+url.QueryEscape(authz.Query().Get("state")))
	if end.Query().Get("connected") != "acct-42" {
		t.Fatalf("connect: %s", end)
	}
	iid := installIDOf(t, a, h.ID, "42")
	if org, _ := repos.InstallOrg(ctx, a.DB, iid); org == "" {
		t.Fatal("installation not claimed")
	}

	// Someone who can't see an installation can't take it by its id.
	if end := connectInstall(t, bobC, "globex", h.ID, "42", "bob"); end.Query().Get("link_error") != "not_yours" {
		t.Fatalf("bob claims 42 without access: %s", end)
	}
	// Someone who can see it still can't take it from another org.
	if end := connectInstall(t, bobC, "globex", h.ID, "42", "bob-also-42"); end.Query().Get("link_error") != "claimed" {
		t.Fatalf("bob takes acme's installation: %s", end)
	}
	if end := connectInstall(t, bobC, "globex", h.ID, "43", "bob"); end.Query().Get("connected") != "acct-43" {
		t.Fatalf("bob connects 43: %s", end)
	}

	// Each org lists its own installations; repositories need the org's claim.
	for _, c := range []struct {
		who  *tc
		org  string
		want string
	}{{root, "acme", "acct-42"}, {bobC, "globex", "acct-43"}} {
		_, l := c.who.do("GET", "/orgs/"+c.org+"/admin/forges/"+h.ID+"/installations", nil)
		items := l["items"].([]any)
		if len(items) != 1 || items[0].(map[string]any)["account_login"] != c.want || l["connect_url"] == nil {
			t.Fatalf("%s installations: %v", c.org, l)
		}
	}
	if code, _ := bobC.do("GET", "/orgs/globex/admin/forges/"+h.ID+"/repositories?installation=42", nil); code != 404 {
		t.Fatalf("globex lists acme's installation repos: %d", code)
	}
	// A state from someone else's session is refused.
	st := step(t, root, "/api/v1/orgs/acme/admin/forges/"+h.ID+"/connect").Query().Get("state")
	if end := step(t, bobC, "/api/v1/admin/forges/github/setup?installation_id=43&state="+url.QueryEscape(st)); end.Query().Get("forge_error") != "expired" {
		t.Fatalf("setup with another person's state: %s", end)
	}
}

func installIDOf(t *testing.T, a *App, hostID, ext string) string {
	t.Helper()
	var id string
	if err := store.QueryRow(context.Background(), a.DB, `SELECT id FROM forge_installs WHERE forge_host_id = ? AND external_id = ?`, hostID, ext).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// In single mode every installation belongs to the default org.
func TestGitHubInstallationsSingleMode(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	gh := fakeGitHub(t, nil)
	h := sharedGitHubHost(t, a, gh)
	code, l := admin.do("GET", "/admin/forges/"+h.ID+"/installations", nil)
	if code != 200 || len(l["items"].([]any)) != 2 || l["connect_url"] != nil {
		t.Fatalf("installations: %d %v", code, l)
	}
	for _, ext := range []string{"42", "43"} {
		if org, _ := repos.InstallOrg(ctx, a.DB, installIDOf(t, a, h.ID, ext)); org != orgs.DefaultID {
			t.Fatalf("installation %s claimed by %q", ext, org)
		}
	}
}
