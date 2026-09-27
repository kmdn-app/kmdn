package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/users"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Priya Raman", "GIT_AUTHOR_EMAIL=priya@northwind.dev", "GIT_COMMITTER_NAME=Priya Raman", "GIT_COMMITTER_EMAIL=priya@northwind.dev")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, dir, p, content string) {
	t.Helper()
	full := filepath.Join(dir, p)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// signIn creates a session for u directly (the sign-in flow has its own tests).
func signIn(t *testing.T, a *App, c *tc, u users.User) {
	t.Helper()
	token, sess, err := a.Auth.CreateSession(context.Background(), a.DB, u.ID, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", c.base, nil)
	rec := &cookieRecorder{h: http.Header{}}
	a.AuthH.SetSessionCookies(rec, token, sess)
	for _, ck := range (&http.Response{Header: rec.h}).Cookies() {
		c.c.Jar.SetCookies(req.URL, []*http.Cookie{ck})
	}
}

type cookieRecorder struct{ h http.Header }

func (c *cookieRecorder) Header() http.Header         { return c.h }
func (c *cookieRecorder) Write(b []byte) (int, error) { return len(b), nil }
func (c *cookieRecorder) WriteHeader(int)             {}

func TestConnectPlainGitRepoAndRead(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	// A local "forge" repository.
	remote := t.TempDir()
	gitIn(t, remote, "init", "--quiet", "-b", "main")
	gitIn(t, remote, "config", "uploadpack.allowFilter", "true")
	gitIn(t, remote, "config", "uploadpack.allowAnySHA1InWant", "true")
	writeFile(t, remote, "docs/onboarding/first-week.md", "# First week\n\nWelcome.\n")
	writeFile(t, remote, "docs/onboarding/images/desk.png", "png")
	writeFile(t, remote, "docs/_archive/old.md", "# Old\n")
	writeFile(t, remote, "src/app.go", "package app\n")
	writeFile(t, remote, ".kmdn.yml", "exclude: [\"docs/_archive/**\"]\nroutes:\n  /docs/: docs/\n")
	gitIn(t, remote, "add", "-A")
	gitIn(t, remote, "commit", "--quiet", "-m", "Create handbook\n\nCo-authored-by: Tom Okafor <tom@northwind.dev>")

	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	tom, _ := users.Create(ctx, a.DB, "tom@northwind.dev", "Tom", false)
	signIn(t, a, admin, maya)

	code, hosts := admin.do("GET", "/admin/forges", nil)
	items := hosts["items"].([]any)
	if code != 200 || len(items) != 1 || items[0].(map[string]any)["kind"] != "git" {
		t.Fatalf("hosts: %d %v", code, hosts)
	}
	hostID := items[0].(map[string]any)["id"].(string)
	code, body := admin.do("POST", "/repos", map[string]any{"forge_host_id": hostID, "clone_url": "file://" + remote, "content_root": "docs/"})
	if code != 202 {
		t.Fatalf("connect: %d %v", code, body)
	}
	repo := body["repo"].(map[string]any)
	repoID := repo["id"].(string)
	if repo["target_branch"] != "main" || repo["health"] != "pending" {
		t.Fatalf("repo: %v", repo)
	}
	// Run the sync job.
	if ran, err := a.Jobs.RunOnce(ctx); !ran || err != nil {
		t.Fatalf("sync job: %v %v", ran, err)
	}
	// The head change schedules the search and link indexes.
	for {
		ran, err := a.Jobs.RunOnce(ctx)
		if err != nil {
			t.Fatalf("index jobs: %v", err)
		}
		if !ran {
			break
		}
	}
	if code, hits := admin.do("GET", "/repos/"+repoID+"/search?q=welc", nil); code != 200 || !strings.Contains(toJSON(hits), "docs/onboarding/first-week.md") || strings.Contains(toJSON(hits), "_archive") {
		t.Fatalf("search: %d %v", code, hits)
	}
	code, repo = admin.do("GET", "/repos/"+repoID, nil)
	if code != 200 || repo["health"] != "ok" || repo["head_sha"] == "" {
		t.Fatalf("after sync: %v", repo)
	}
	scope := repo["scope"].(map[string]any)
	if scope["root"] != "docs" || !strings.Contains(toJSON(scope["from_file"]), "exclude") {
		t.Fatalf("scope: %v", scope)
	}
	code, tree := admin.do("GET", "/repos/"+repoID+"/tree", nil)
	js := toJSON(tree)
	if code != 200 || !strings.Contains(js, "docs/onboarding/first-week.md") || !strings.Contains(js, "desk.png") || strings.Contains(js, "_archive") || strings.Contains(js, "src/app.go") {
		t.Fatalf("tree: %s", js)
	}
	code, f := admin.do("GET", "/repos/"+repoID+"/files/docs/onboarding/first-week.md", nil)
	if code != 200 || f["content"] != "# First week\n\nWelcome.\n" || f["markdown"] != true {
		t.Fatalf("file: %d %v", code, f)
	}
	// The web client encodes "/" in the path parameter.
	if code, f := admin.do("GET", "/repos/"+repoID+"/files/docs%2Fonboarding%2Ffirst-week.md", nil); code != 200 || f["path"] != "docs/onboarding/first-week.md" {
		t.Fatalf("escaped path: %d %v", code, f)
	}
	if code, _ := admin.do("GET", "/repos/"+repoID+"/files/docs%2F..%2Fsrc%2Fapp.go", nil); code != 404 {
		t.Fatalf("traversal: %d", code)
	}
	if code, _ := admin.do("GET", "/repos/"+repoID+"/files/src/app.go", nil); code != 404 {
		t.Fatalf("out of scope file: %d", code)
	}
	code, hist := admin.do("GET", "/repos/"+repoID+"/history/docs/onboarding/first-week.md", nil)
	if code != 200 || !strings.Contains(toJSON(hist), "Tom Okafor") {
		t.Fatalf("history: %v", hist)
	}
	if code, act := admin.do("GET", "/repos/"+repoID+"/activity", nil); code != 200 || !strings.Contains(toJSON(act), "Create handbook") || strings.Contains(toJSON(act), "src/app.go") {
		t.Fatalf("activity: %d %v", code, act)
	}
	rawRes, err := admin.c.Get(admin.base + "/api/v1/repos/" + repoID + "/raw/docs/onboarding/images/desk.png")
	if err != nil {
		t.Fatal(err)
	}
	rawRes.Body.Close()
	if rawRes.StatusCode != 200 || rawRes.Header.Get("Content-Type") != "image/png" || !strings.Contains(rawRes.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("raw: %d %v", rawRes.StatusCode, rawRes.Header)
	}
	if code, bl := admin.do("GET", "/repos/"+repoID+"/blame/docs/onboarding/first-week.md", nil); code != 200 || len(bl["items"].([]any)) != 3 {
		t.Fatalf("blame: %v", bl)
	}

	// Tom can't see the repo until granted a role, then can read but not change settings.
	tc2 := &tc{t: t, base: admin.base, c: newClient()}
	signIn(t, a, tc2, tom)
	if code, _ := tc2.do("GET", "/repos/"+repoID, nil); code != 404 {
		t.Fatalf("unauthorized repo visible: %d", code)
	}
	if code, body := admin.do("PUT", "/repos/"+repoID+"/members/user/"+tom.ID, map[string]string{"role": "contributor"}); code != 200 {
		t.Fatalf("grant: %d %v", code, body)
	}
	code, list := tc2.do("GET", "/repos", nil)
	if code != 200 || len(list["items"].([]any)) != 1 || list["items"].([]any)[0].(map[string]any)["role"] != "contributor" {
		t.Fatalf("tom's repos: %v", list)
	}
	if code, _ := tc2.do("PATCH", "/repos/"+repoID, map[string]any{"display_name": "Nope"}); code != 403 {
		t.Fatalf("contributor changed settings: %d", code)
	}
	if code, _ := tc2.do("POST", "/repos", map[string]any{"forge_host_id": hostID, "clone_url": "file://" + remote}); code != 403 {
		t.Fatalf("contributor connected a repo: %d", code)
	}

	// Push webhook for plain git schedules a sync.
	r, _ := repos.Get(ctx, a.DB, repoID)
	hookURL, secret, _ := a.Repos.WebhookInfo(ctx, r)
	if !strings.HasSuffix(hookURL, "/hooks/git/"+repoID) || secret == "" {
		t.Fatalf("webhook info: %s", hookURL)
	}
	writeFile(t, remote, "docs/new.md", "# New\n")
	gitIn(t, remote, "add", "-A")
	newHead := gitIn(t, remote, "commit", "--quiet", "-m", "Add page")
	_ = newHead
	req, _ := http.NewRequest("POST", admin.base+"/hooks/git/"+repoID, strings.NewReader(`{"branch":"main"}`))
	req.Header.Set("X-Kmdn-Token", "wrong")
	res, _ := http.DefaultClient.Do(req)
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("bad token accepted: %d", res.StatusCode)
	}
	req, _ = http.NewRequest("POST", admin.base+"/hooks/git/"+repoID, strings.NewReader(`{"branch":"main"}`))
	req.Header.Set("X-Kmdn-Token", secret)
	req.Header.Set("X-Kmdn-Delivery", "d1")
	res, _ = http.DefaultClient.Do(req)
	res.Body.Close()
	if res.StatusCode != 202 {
		t.Fatalf("webhook: %d", res.StatusCode)
	}
	if ran, err := a.Jobs.RunOnce(ctx); !ran || err != nil {
		t.Fatalf("webhook sync: %v %v", ran, err)
	}
	// The head change schedules the search and link indexes.
	for {
		ran, err := a.Jobs.RunOnce(ctx)
		if err != nil {
			t.Fatalf("reindex: %v", err)
		}
		if !ran {
			break
		}
	}
	if _, hits := admin.do("GET", "/repos/"+repoID+"/search?q=new", nil); !strings.Contains(toJSON(hits), "docs/new.md") {
		t.Fatalf("new page not indexed: %v", hits)
	}
	if _, tree := admin.do("GET", "/repos/"+repoID+"/tree", nil); !strings.Contains(toJSON(tree), "docs/new.md") {
		t.Fatalf("new file not synced: %v", tree)
	}

	// Settings: change content root; disconnect.
	if code, repo := admin.do("PATCH", "/repos/"+repoID, map[string]any{"display_name": "Northwind handbook", "settings": map[string]any{"allow_self_approval": true}}); code != 200 || repo["display_name"] != "Northwind handbook" {
		t.Fatalf("patch: %d %v", code, repo)
	}
	if code, _ := admin.do("DELETE", "/repos/"+repoID, nil); code != 204 {
		t.Fatalf("disconnect: %d", code)
	}
	if code, _ := admin.do("GET", "/repos/"+repoID, nil); code != 404 {
		t.Fatalf("after disconnect: %d", code)
	}
	_ = auth.CSRFHeader
}

// A token the forge refuses (from another GitLab, expired, revoked) is the
// person's to fix: a 422 on the token field naming the host, not a 500.
func TestConnectGitLabRefusedToken(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	gl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"401 Unauthorized"}`))
	}))
	defer gl.Close()
	code, h := admin.do("POST", "/admin/forges", map[string]any{"kind": "gitlab", "base_url": gl.URL})
	if code != 201 {
		t.Fatalf("add gitlab: %d %v", code, h)
	}
	code, body := admin.do("POST", "/repos", map[string]any{"forge_host_id": h["id"], "owner": "advocacy", "name": "dispatch-kb", "token": "glpat-elsewhere"})
	host := strings.TrimPrefix(gl.URL, "http://")
	if msg := toJSON(body); code != 422 || !strings.Contains(msg, "token") || !strings.Contains(msg, host+" refused this token (401)") {
		t.Fatalf("connect: %d %s", code, msg)
	}
}
