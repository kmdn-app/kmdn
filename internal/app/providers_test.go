package app

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/config"
	"github.com/kmdn-app/kmdn/internal/groups"
	"github.com/kmdn-app/kmdn/internal/mail"
	"github.com/kmdn-app/kmdn/internal/orgs"
	"github.com/kmdn-app/kmdn/internal/policy"
	"github.com/kmdn-app/kmdn/internal/provision"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/storetest"
	"github.com/kmdn-app/kmdn/internal/users"
)

// fakeIdP is a sign-in provider an embedder would add: it sends people to
// an identity provider and trusts whatever identity the test sets.
type fakeIdP struct{ who auth.Identity }

func (p *fakeIdP) Info() auth.ProviderInfo {
	return auth.ProviderInfo{ID: "acme-sso", Name: "Acme SSO", Listed: true}
}

func (p *fakeIdP) Start(w http.ResponseWriter, r *http.Request, state string) error {
	http.Redirect(w, r, "https://idp.acme.dev/sso?RelayState="+url.QueryEscape(state), http.StatusFound)
	return nil
}

func (p *fakeIdP) Callback(r *http.Request) (auth.Identity, string, error) {
	if r.FormValue("fail") != "" {
		return auth.Identity{}, "", errors.New("idp said no")
	}
	return p.who, r.FormValue("RelayState"), nil
}

func newAppWith(t *testing.T, mutate func(*config.Config), opts Options) (*App, *tc) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.DB.URL = "sqlite://" + filepath.Join(dir, "kmdn.db")
	if storetest.PostgresEnabled() {
		cfg.DB.URL = storetest.PostgresURL(t)
	}
	cfg.SecretKey = base64.StdEncoding.EncodeToString(make([]byte, 32))
	if mutate != nil {
		mutate(&cfg)
	}
	a, err := NewWith(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	srv := httptest.NewServer(a.Server.Handler())
	t.Cleanup(srv.Close)
	return a, &tc{t: t, base: srv.URL, c: newClient()}
}

// An embedder's provider signs people in (creating their account in the
// provider's org), and the org's sign-in policy refuses other methods for
// its members, except owners.
func TestSignInProviderAndPolicy(t *testing.T) {
	idp := &fakeIdP{}
	var acmeID string
	pol := &policy.Policy{SignIn: func(_ context.Context, orgID string, _ policy.Subject, method string) *policy.SignInRequired {
		if orgID != acmeID || method == auth.ProviderMethod("acme-sso") {
			return nil
		}
		return &policy.SignInRequired{Message: "Acme signs in with Acme SSO.", URL: auth.ProviderStartURL("acme-sso", "/acme")}
	}}
	a, olgaC := newAppWith(t, multiOrgs, Options{Policy: pol, Providers: []auth.Provider{idp}})
	ctx := context.Background()
	capture := &mail.Capture{}
	a.Auth.Mail = capture
	olga, _ := users.Create(ctx, a.DB, "olga@acme.dev", "Olga", false)
	signIn(t, a, olgaC, olga)
	_, org := olgaC.do("POST", "/orgs", map[string]any{"name": "Acme", "slug": "acme"})
	acmeID = org["id"].(string)
	repoID, _ := connectLocalIn(t, a, olgaC, "acme", map[string]string{"docs/index.md": "# Acme\n"})

	if _, list := olgaC.do("GET", "/auth/providers", nil); toJSON(list) != `{"items":[{"id":"acme-sso","name":"Acme SSO","start_url":"/api/v1/auth/providers/acme-sso/start"}]}` {
		t.Fatalf("providers: %v", list)
	}

	// Pat has no account; Acme's identity provider vouches for them.
	idp.who = auth.Identity{Issuer: "https://idp.acme.dev", Subject: "u-1", Email: "Pat@acme.dev", EmailVerified: true, Name: "Pat", OrgID: acmeID, Create: true}
	patC := &tc{t: t, base: olgaC.base, c: newClient()}
	patC.c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	signInWithIdP := func(c *tc, query string) *http.Response {
		t.Helper()
		res, err := c.c.Get(c.base + "/api/v1/auth/providers/acme-sso/start?redirect=/acme")
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		loc, _ := url.Parse(res.Header.Get("Location"))
		if res.StatusCode != 302 || loc.Host != "idp.acme.dev" {
			t.Fatalf("start: %d %s", res.StatusCode, loc)
		}
		// The identity provider posts back, as SAML does.
		form := url.Values{"RelayState": {loc.Query().Get("RelayState")}}
		if query != "" {
			form.Set("RelayState", query)
		}
		res, err = c.c.PostForm(c.base+"/api/v1/auth/providers/acme-sso/callback", form)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}
	if res := signInWithIdP(patC, "forged-state"); res.Header.Get("Location") != "/signin?oauth_error=expired" {
		t.Fatalf("forged state: %s", res.Header.Get("Location"))
	}
	if res := signInWithIdP(patC, ""); res.StatusCode != 302 || res.Header.Get("Location") != "/acme" {
		t.Fatalf("callback: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if code, o := patC.do("GET", "/orgs/acme", nil); code != 200 || o["role"] != "member" {
		t.Fatalf("pat in acme: %d %v", code, o)
	}
	pat, err := users.ByEmail(ctx, a.DB, "pat@acme.dev")
	if err != nil || pat.Name != "Pat" {
		t.Fatalf("pat's account: %v %+v", err, pat)
	}
	if _, s := patC.do("GET", "/me/sessions", nil); !strings.Contains(toJSON(s), `"method":"provider:acme-sso"`) {
		t.Fatalf("session method: %v", s)
	}
	// The same identity signs in again through its link, whatever the email.
	idp.who.Email = "pat.renamed@acme.dev"
	again := &tc{t: t, base: olgaC.base, c: newClient()}
	again.c.CheckRedirect = patC.c.CheckRedirect
	if res := signInWithIdP(again, ""); res.Header.Get("Location") != "/acme" {
		t.Fatalf("second sign-in: %s", res.Header.Get("Location"))
	}
	var links int
	_ = store.QueryRow(ctx, a.DB, `SELECT COUNT(*) FROM identity_links WHERE user_id = ?`, pat.ID).Scan(&links)
	if links != 1 {
		t.Fatalf("identity links: %d", links)
	}

	// A magic link doesn't sign Pat in: Acme is their only org.
	if code, _ := patC.do("POST", "/auth/magic-link", map[string]any{"email": "pat@acme.dev"}); code != 202 {
		t.Fatalf("request link: %d", code)
	}
	m, _ := capture.Last()
	tok := strings.SplitN(strings.SplitN(m.Text, "token=", 2)[1], "\n", 2)[0]
	tok = strings.TrimRight(strings.Fields(tok)[0], ".")
	code, body := (&tc{t: t, base: olgaC.base, c: newClient()}).do("POST", "/auth/magic-link/verify", map[string]any{"token": tok})
	if code != 403 || body["code"] != "sign_in_required" || body["detail"] != "Acme signs in with Acme SSO." {
		t.Fatalf("magic link for an SSO org: %d %v", code, body)
	}

	// A session from another method can't act in Acme: the org explains,
	// ID-addressed resources are hidden, and the org list says why.
	mlC := &tc{t: t, base: olgaC.base, c: newClient()}
	signInBy(t, a, mlC, pat, "magic_link")
	code, body = mlC.do("GET", "/orgs/acme/repos", nil)
	if code != 403 || body["code"] != "sign_in_required" || body["params"].(map[string]any)["sign_in_url"] != "/api/v1/auth/providers/acme-sso/start?redirect=%2Facme" {
		t.Fatalf("org with the wrong session: %d %v", code, body)
	}
	if code, _ := mlC.do("GET", "/repos/"+repoID, nil); code != 404 {
		t.Fatalf("repo with the wrong session: %d", code)
	}
	if _, list := mlC.do("GET", "/orgs", nil); !strings.Contains(toJSON(list), `"role":"member","sign_in":{"message":"Acme signs in with Acme SSO.","org_id":"`+acmeID+`"`) {
		t.Fatalf("org list: %v", list)
	}
	// Owners always get in (break-glass), whatever their session.
	olgaML := &tc{t: t, base: olgaC.base, c: newClient()}
	signInBy(t, a, olgaML, olga, "magic_link")
	if code, _ := olgaML.do("GET", "/repos/"+repoID, nil); code != 200 {
		t.Fatalf("owner with a magic link: %d", code)
	}
	// Someone outside Acme still gets 404, not a sign-in hint.
	eve, _ := users.Create(ctx, a.DB, "eve@evil.dev", "Eve", false)
	eveC := &tc{t: t, base: olgaC.base, c: newClient()}
	signInBy(t, a, eveC, eve, "magic_link")
	if code, _ := eveC.do("GET", "/orgs/acme", nil); code != 404 {
		t.Fatalf("outsider: %d", code)
	}
}

func signInBy(t *testing.T, a *App, c *tc, u users.User, method string) {
	t.Helper()
	token, sess, err := a.Auth.CreateSessionBy(context.Background(), a.DB, u.ID, "127.0.0.1", "test", method)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", c.base, nil)
	rec := &cookieRecorder{h: http.Header{}}
	a.AuthH.SetSessionCookies(rec, token, sess)
	c.c.Jar.SetCookies(req.URL, (&http.Response{Header: rec.h}).Cookies())
}

// A directory sync adds, changes and removes members and group members
// without invitations, within the org's limits, and audits each change.
func TestProvisioning(t *testing.T) {
	a, root := newApp(t, multiOrgs)
	ctx := context.Background()
	olga, _ := users.Create(ctx, a.DB, "olga@acme.dev", "Olga", false)
	signIn(t, a, root, olga)
	_, org := root.do("POST", "/orgs", map[string]any{"name": "Acme", "slug": "acme"})
	acme := org["id"].(string)
	p := a.Provision

	carl, err := p.Upsert(ctx, acme, "scim", provision.Member{Email: "Carl@acme.dev", Name: "Carl", Role: orgs.Admin, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	if role, _ := orgs.Role(ctx, a.DB, acme, carl); role != orgs.Admin {
		t.Fatalf("carl: %q", role)
	}
	if _, err := p.Upsert(ctx, acme, "scim", provision.Member{Email: "carl@acme.dev", Active: false}); err != nil {
		t.Fatal(err)
	}
	if role, _ := orgs.Role(ctx, a.DB, acme, carl); role != "" {
		t.Fatalf("deactivated carl: %q", role)
	}
	if _, err := p.Upsert(ctx, acme, "scim", provision.Member{Email: "olga@acme.dev", Role: orgs.Member, Active: true}); !errors.Is(err, provision.ErrLastOwner) {
		t.Fatalf("demote the last owner: %v", err)
	}
	dana, _ := p.Upsert(ctx, acme, "scim", provision.Member{Email: "dana@acme.dev", Active: true})

	g, err := p.EnsureGroup(ctx, acme, "scim", "Engineering")
	if err != nil {
		t.Fatal(err)
	}
	if g2, _ := p.EnsureGroup(ctx, acme, "scim", "engineering"); g2.ID != g.ID {
		t.Fatal("EnsureGroup made a second group")
	}
	if err := p.SetGroupMembers(ctx, acme, "scim", g.ID, []string{dana.ID, carl.ID}); err != nil {
		t.Fatal(err)
	}
	if err := p.SetGroupMembers(ctx, acme, "scim", g.ID, []string{dana.ID, olga.ID}); err != nil {
		t.Fatal(err)
	}
	if ids, _ := groups.MemberIDs(ctx, a.DB, g.ID); len(ids) != 2 {
		t.Fatalf("group members: %v", ids)
	}
	if err := p.SetGroupMembers(ctx, acme, "scim", g.ID, []string{"usr_nobody"}); err == nil {
		t.Fatal("added someone outside the org to a group")
	}
	if err := p.Remove(ctx, acme, "scim", dana.ID); err != nil {
		t.Fatal(err)
	}
	if ids, _ := groups.MemberIDs(ctx, a.DB, g.ID); len(ids) != 1 {
		t.Fatalf("group after removal: %v", ids)
	}

	// Limits apply to people a sync adds (Olga holds the only seat: Carl is
	// deactivated, Dana gone).
	a.Policy.Limits = func(context.Context, string) policy.Limits { return policy.Limits{Members: 1} }
	if _, err := p.Upsert(ctx, acme, "scim", provision.Member{Email: "erin@acme.dev", Active: true}); err == nil {
		t.Fatal("went over the member limit")
	} else if _, ok := policy.IsLimit(err); !ok {
		t.Fatalf("limit error: %v", err)
	}
	var n int
	_ = store.QueryRow(ctx, a.DB, `SELECT COUNT(*) FROM audit_log WHERE org_id = ? AND data LIKE '%"via":"scim"%'`, acme).Scan(&n)
	if n < 6 {
		t.Fatalf("audited changes: %d", n)
	}
}
