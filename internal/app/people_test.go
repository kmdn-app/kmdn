package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/mail"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

func seedRepo(t *testing.T, a *App, name string) repos.Repo {
	t.Helper()
	ctx := context.Background()
	h, err := repos.EnsureGitHost(ctx, a.DB)
	if err != nil {
		t.Fatal(err)
	}
	id := "rep_" + name
	if _, err := store.Exec(ctx, a.DB, `INSERT INTO repos (id, forge_host_id, owner, name, display_name, target_branch, created_at) VALUES (?, ?, 'northwind', ?, ?, 'main', ?)`,
		id, h.ID, name, "Northwind "+name, store.Millis(time.Now())); err != nil {
		t.Fatal(err)
	}
	r, err := repos.Get(ctx, a.DB, id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

var inviteRe = regexp.MustCompile(`/invite/([A-Za-z0-9_-]+)`)

func TestInviteToRepoAndAccept(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	capture := &mail.Capture{}
	a.Invites.Mail = capture
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya Chen", true)
	signIn(t, a, admin, maya)
	repo := seedRepo(t, a, "handbook")

	code, body := admin.do("POST", "/repos/"+repo.ID+"/invites", map[string]string{"email": "Tom@Northwind.dev", "role": "contributor"})
	if code != 201 || body["status"] != "invited" {
		t.Fatalf("invite: %d %v", code, body)
	}
	m, ok := capture.Last()
	if !ok || m.To != "tom@northwind.dev" || !strings.Contains(m.Subject, "Maya Chen invited you to Northwind handbook") {
		t.Fatalf("mail: %+v", m)
	}
	token := inviteRe.FindStringSubmatch(m.Text)[1]

	guest := &tc{t: t, base: admin.base, c: newClient()}
	code, d := guest.do("GET", "/invites/"+token, nil)
	if code != 200 || d["repo_name"] != "Northwind handbook" || d["inviter_name"] != "Maya Chen" || d["role"] != "contributor" || d["has_account"] != false {
		t.Fatalf("details: %d %v", code, d)
	}
	code, u := guest.do("POST", "/invites/"+token+"/accept", map[string]string{"name": "Tom Okafor"})
	if code != 200 || u["name"] != "Tom Okafor" {
		t.Fatalf("accept: %d %v", code, u)
	}
	if code, me := guest.do("GET", "/me", nil); code != 200 || me["email"] != "tom@northwind.dev" {
		t.Fatalf("signed in after accept: %d %v", code, me)
	}
	tom, _ := users.ByEmail(ctx, a.DB, "tom@northwind.dev")
	if role, _ := access.Effective(ctx, a.DB, tom, repo.ID); role != access.Contributor {
		t.Fatalf("role after accept: %q", role)
	}
	if code, _ := guest.do("GET", "/invites/"+token, nil); code != 404 {
		t.Fatalf("invite reusable: %d", code)
	}
	// Existing account: granted directly, no email.
	before := len(capture.Sent)
	code, body = admin.do("POST", "/repos/"+repo.ID+"/invites", map[string]string{"email": "tom@northwind.dev", "role": "maintainer"})
	if code != 201 || body["status"] != "granted" || len(capture.Sent) != before {
		t.Fatalf("existing user invite: %d %v", code, body)
	}
	if role, _ := access.Effective(ctx, a.DB, tom, repo.ID); role != access.Maintainer {
		t.Fatalf("role after grant: %q", role)
	}
	// Contributors can't invite.
	if code, _ := guest.do("POST", "/repos/"+repo.ID+"/invites", map[string]string{"email": "x@y.z", "role": "viewer"}); code != 403 {
		t.Fatalf("maintainer invited (needs admin): %d", code)
	}
}

func TestAdminUserGuardsAndGroups(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya Chen", true)
	tom, _ := users.Create(ctx, a.DB, "tom@northwind.dev", "Tom Okafor", false)
	signIn(t, a, admin, maya)

	if code, b := admin.do("PATCH", "/admin/users/"+maya.ID, map[string]any{"is_instance_admin": false}); code != 409 || !strings.Contains(toJSON(b), "last_admin") {
		t.Fatalf("demote last admin: %d %v", code, b)
	}
	if code, _ := admin.do("PATCH", "/admin/users/"+maya.ID, map[string]any{"status": "deactivated"}); code != 409 {
		t.Fatalf("deactivate self: %d", code)
	}
	tomClient := &tc{t: t, base: admin.base, c: newClient()}
	signIn(t, a, tomClient, tom)
	if code, _ := tomClient.do("GET", "/admin/users", nil); code != 403 {
		t.Fatalf("non-admin listed users: %d", code)
	}
	if code, b := admin.do("PATCH", "/admin/users/"+tom.ID, map[string]any{"status": "deactivated"}); code != 200 || b["status"] != "deactivated" {
		t.Fatalf("deactivate tom: %d %v", code, b)
	}
	if code, _ := tomClient.do("GET", "/me", nil); code != 401 {
		t.Fatalf("deactivated session still valid: %d", code)
	}
	code, list := admin.do("GET", "/admin/users?q=okaf", nil)
	if code != 200 || len(list["items"].([]any)) != 1 {
		t.Fatalf("search: %v", list)
	}

	code, g := admin.do("POST", "/admin/groups", map[string]string{"name": "People team", "description": "HR"})
	if code != 201 {
		t.Fatalf("create group: %d %v", code, g)
	}
	gid := g["id"].(string)
	if code, _ := admin.do("POST", "/admin/groups", map[string]string{"name": "People team"}); code != 422 {
		t.Fatalf("duplicate group: %d", code)
	}
	if code, m := admin.do("PUT", "/admin/groups/"+gid+"/members/"+tom.ID, nil); code != 200 || len(m["items"].([]any)) != 1 {
		t.Fatalf("add member: %d %v", code, m)
	}
	if code, gl := admin.do("GET", "/admin/groups", nil); code != 200 || gl["items"].([]any)[0].(map[string]any)["members"] != float64(1) {
		t.Fatalf("groups: %v", gl)
	}
	if code, al := admin.do("GET", "/admin/audit", nil); code != 200 || !strings.Contains(toJSON(al), "group.created") {
		t.Fatalf("audit: %v", al)
	}
}

func TestSignInWithGitHubMatchesVerifiedEmail(t *testing.T) {
	a, _ := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya Chen", true)
	fake := http.NewServeMux()
	fake.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("code") != "good" || r.Form.Get("client_secret") != "cs" {
			_, _ = w.Write([]byte(`{"error":"bad_verification_code"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"gho_x"}`))
	})
	fake.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":1203344,"login":"mayachen","name":"Maya Chen","avatar_url":"https://avatars/x"}`))
	})
	fake.HandleFunc("GET /user/emails", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"email":"maya@personal.example","primary":false,"verified":false},{"email":"Maya@Northwind.dev","primary":true,"verified":true}]`))
	})
	gh := httptest.NewServer(fake)
	defer gh.Close()
	var hostID string
	err := a.DB.InTx(ctx, func(tx *store.Tx) error {
		h, err := repos.CreateHost(ctx, tx, a.Secrets, repos.HostInput{Kind: "github", BaseURL: gh.URL, APIURL: gh.URL, DisplayName: "GitHub", ClientID: "Iv1.x", ClientSecret: "cs"})
		hostID = h.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	c := &tc{t: t, base: "", c: newClient()}
	srv := httptest.NewServer(a.Server.Handler())
	defer srv.Close()
	c.base = srv.URL
	c.c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	if code, p := c.do("GET", "/auth/oauth/providers", nil); code != 200 || !strings.Contains(toJSON(p), hostID) {
		t.Fatalf("providers: %v", p)
	}
	res, err := c.c.Get(srv.URL + "/api/v1/auth/oauth/" + hostID + "/start?redirect=/northwind")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	loc, _ := url.Parse(res.Header.Get("Location"))
	if res.StatusCode != 302 || !strings.HasPrefix(loc.String(), gh.URL+"/login/oauth/authorize") || loc.Query().Get("client_id") != "Iv1.x" {
		t.Fatalf("start redirect: %d %s", res.StatusCode, loc)
	}
	state := loc.Query().Get("state")
	// A forged callback without the browser's state cookie fails.
	other := newClient()
	other.CheckRedirect = c.c.CheckRedirect
	res, _ = other.Get(srv.URL + "/api/v1/auth/oauth/" + hostID + "/callback?code=good&state=" + state)
	res.Body.Close()
	if !strings.Contains(res.Header.Get("Location"), "oauth_error=expired") {
		t.Fatalf("callback without cookie: %s", res.Header.Get("Location"))
	}
	res, err = c.c.Get(srv.URL + "/api/v1/auth/oauth/" + hostID + "/callback?code=good&state=" + state)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.Header.Get("Location") != "/northwind" {
		t.Fatalf("callback redirect: %s", res.Header.Get("Location"))
	}
	code, me := c.do("GET", "/me", nil)
	if code != 200 || me["id"] != maya.ID {
		t.Fatalf("signed in as: %d %v", code, me)
	}
	code, la := c.do("GET", "/me/linked-accounts", nil)
	var accts []map[string]any
	b, _ := json.Marshal(la["items"])
	_ = json.Unmarshal(b, &accts)
	if code != 200 || len(accts) != 1 || accts[0]["login"] != "mayachen" || accts[0]["noreply_email"] != "1203344+mayachen@users.noreply."+strings.TrimPrefix(gh.URL, "http://") {
		t.Fatalf("linked accounts: %v", la)
	}
	if code, _ := c.do("PUT", "/me/commit-email", map[string]string{"mode": "custom", "custom": "maya.chen@northwind.dev"}); code != 204 {
		t.Fatalf("commit email: %d", code)
	}
}

// GitLab OAuth applications are registered by hand before kmdn knows the
// host's id, so GitLab uses one fixed redirect URI, shown in Admin → Forges.
func TestSignInWithGitLabFixedCallback(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya Chen", true)
	c := &tc{t: t, base: "", c: newClient()}
	srv := httptest.NewServer(a.Server.Handler())
	defer srv.Close()
	want := strings.TrimRight(a.Repos.BaseURL, "/") + "/api/v1/auth/oauth/callback"
	fake := http.NewServeMux()
	fake.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("code") != "good" || r.Form.Get("client_secret") != "gls" || r.Form.Get("redirect_uri") != want || r.Form.Get("grant_type") != "authorization_code" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"glo_x"}`))
	})
	fake.HandleFunc("GET /api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":42,"username":"maya","name":"Maya Chen","email":"maya@northwind.dev","confirmed_at":"2026-01-01T00:00:00Z"}`))
	})
	gl := httptest.NewServer(fake)
	defer gl.Close()

	signIn(t, a, admin, maya)
	if _, v := admin.do("GET", "/admin/forges", nil); v["gitlab_callback_url"] != want {
		t.Fatalf("callback url: %v", v)
	}
	code, h := admin.do("POST", "/admin/forges", map[string]any{"kind": "gitlab", "base_url": gl.URL, "client_id": "glid", "client_secret": "gls"})
	if code != 201 {
		t.Fatalf("add gitlab: %d %v", code, h)
	}
	c.base = srv.URL
	c.c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := c.c.Get(srv.URL + "/api/v1/auth/oauth/" + h["id"].(string) + "/start?redirect=/northwind")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	loc, _ := url.Parse(res.Header.Get("Location"))
	if !strings.HasPrefix(loc.String(), gl.URL+"/oauth/authorize") || loc.Query().Get("redirect_uri") != want || loc.Query().Get("scope") != "read_user" {
		t.Fatalf("start redirect: %s", loc)
	}
	res, err = c.c.Get(srv.URL + "/api/v1/auth/oauth/callback?code=good&state=" + loc.Query().Get("state"))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.Header.Get("Location") != "/northwind" {
		t.Fatalf("callback redirect: %s", res.Header.Get("Location"))
	}
	if code, me := c.do("GET", "/me", nil); code != 200 || me["id"] != maya.ID {
		t.Fatalf("signed in as: %d %v", code, me)
	}
}
