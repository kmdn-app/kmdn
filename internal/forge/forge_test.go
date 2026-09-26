package forge

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGitHubAppTokenRepoAndProtection(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	var tokenCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /app/installations/42/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		jwt := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		parts := strings.Split(jwt, ".")
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		h := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, h[:], sig) != nil {
			http.Error(w, `{"message":"bad jwt"}`, http.StatusUnauthorized)
			return
		}
		var claims struct {
			Iss string `json:"iss"`
		}
		b, _ := base64.RawURLEncoding.DecodeString(parts[1])
		_ = json.Unmarshal(b, &claims)
		if claims.Iss != "123" {
			http.Error(w, `{"message":"wrong iss"}`, http.StatusUnauthorized)
			return
		}
		tokenCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_test", "expires_at": time.Now().Add(time.Hour)})
	})
	requireToken := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "token ghs_test" {
			http.Error(w, `{"message":"no"}`, http.StatusUnauthorized)
			return false
		}
		return true
	}
	mux.HandleFunc("GET /repos/northwind/handbook/installation", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":42}`))
	})
	mux.HandleFunc("GET /repos/northwind/handbook", func(w http.ResponseWriter, r *http.Request) {
		if requireToken(w, r) {
			_, _ = w.Write([]byte(`{"id":987,"name":"handbook","private":true,"default_branch":"main","clone_url":"https://github.com/northwind/handbook.git","html_url":"https://github.com/northwind/handbook","owner":{"login":"northwind"}}`))
		}
	})
	mux.HandleFunc("GET /repos/northwind/handbook/branches/main", func(w http.ResponseWriter, r *http.Request) {
		if requireToken(w, r) {
			_, _ = w.Write([]byte(`{"name":"main","protected":true}`))
		}
	})
	mux.HandleFunc("GET /repos/northwind/handbook/rules/branches/main", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"type":"pull_request","parameters":{"required_approving_review_count":1}},{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"ci"},{"context":"lint"}]}}]`))
	})
	mux.HandleFunc("GET /repos/northwind/handbook/branches/main/protection", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Resource not accessible by integration"}`, http.StatusForbidden)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := &GitHubApp{APIURL: srv.URL, AppID: "123", PrivateKey: key}
	ctx := context.Background()
	repo := Repo{Owner: "northwind", Name: "handbook"}
	info, err := g.RepoInfo(ctx, repo)
	if err != nil || info.ExternalID != "987" || info.DefaultBranch != "main" || !info.Private {
		t.Fatalf("repo info: %+v %v", info, err)
	}
	cred, err := g.Credential(ctx, repo)
	if err != nil || cred.Password != "ghs_test" || cred.Username != "x-access-token" {
		t.Fatalf("credential: %+v %v", cred, err)
	}
	if tokenCalls.Load() != 1 {
		t.Fatalf("installation token not cached: %d calls", tokenCalls.Load())
	}
	p, err := g.BranchProtection(ctx, repo, "main")
	if err != nil || !p.Protected || p.RequiredReviews != 1 || p.RequiredChecks != 2 || !strings.Contains(p.Detail, "pull request") {
		t.Fatalf("protection: %+v %v", p, err)
	}
	if _, err := g.RepoInfo(ctx, Repo{Owner: "northwind", Name: "nope", InstallID: "42"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing repo: %v", err)
	}
}

func sign(body, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestGitHubWebhook(t *testing.T) {
	g := &GitHubApp{}
	body := `{"ref":"refs/heads/main","after":"abc123","repository":{"id":987,"name":"handbook","owner":{"login":"northwind"}},"installation":{"id":42}}`
	r := httptest.NewRequest("POST", "/hooks/github", nil)
	r.Header.Set("X-GitHub-Event", "push")
	r.Header.Set("X-GitHub-Delivery", "d-1")
	r.Header.Set("X-Hub-Signature-256", sign(body, "whsec"))
	ev, err := g.ParseWebhook(r, []byte(body), "whsec")
	if err != nil || ev.Type != "push" || ev.Branch != "main" || ev.After != "abc123" || ev.Repo.ExternalID != "987" || ev.Repo.InstallID != "42" || ev.DeliveryID != "d-1" {
		t.Fatalf("%+v %v", ev, err)
	}
	r.Header.Set("X-Hub-Signature-256", sign(body, "other"))
	if _, err := g.ParseWebhook(r, []byte(body), "whsec"); !errors.Is(err, ErrBadSignature) {
		t.Fatal("bad signature accepted")
	}
	inst := `{"action":"deleted","installation":{"id":42,"account":{"login":"northwind"}}}`
	r = httptest.NewRequest("POST", "/hooks/github", nil)
	r.Header.Set("X-GitHub-Event", "installation")
	r.Header.Set("X-Hub-Signature-256", sign(inst, "whsec"))
	ev, err = g.ParseWebhook(r, []byte(inst), "whsec")
	if err != nil || ev.Type != "installation" || !ev.Removed || ev.Install.AccountLogin != "northwind" {
		t.Fatalf("installation: %+v %v", ev, err)
	}
}

func TestManifestConversion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/app-manifests/code-1/conversions" || r.Method != "POST" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"id":55,"slug":"kmdn-northwind","pem":"-----BEGIN RSA PRIVATE KEY-----","webhook_secret":"ws","client_id":"Iv1.x","client_secret":"cs"}`))
	}))
	defer srv.Close()
	c, err := ConvertManifest(context.Background(), nil, srv.URL, "code-1")
	if err != nil || c.ID != 55 || c.Slug != "kmdn-northwind" || c.WebhookSecret != "ws" {
		t.Fatalf("%+v %v", c, err)
	}
	m := AppManifest("https://kmdn.example.com/", "kmdn", "fh_1")
	if m["redirect_url"] != "https://kmdn.example.com/api/v1/admin/forges/github/callback" {
		t.Fatal(m["redirect_url"])
	}
	if GitHubAPIURL("https://github.com") != "https://api.github.com" || GitHubAPIURL("https://ghe.example.com/") != "https://ghe.example.com/api/v3" {
		t.Fatal("api url")
	}
}

func TestGitLab(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v4/projects/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "glpat" {
			http.Error(w, `{"message":"401 Unauthorized"}`, http.StatusUnauthorized)
			return
		}
		if r.PathValue("id") != "platform/runbooks" && r.PathValue("id") != "77" {
			http.Error(w, `{"message":"404 Project Not Found"}`, 404)
			return
		}
		_, _ = w.Write([]byte(`{"id":77,"path":"runbooks","path_with_namespace":"platform/runbooks","default_branch":"main","visibility":"internal","http_url_to_repo":"https://gitlab.example.com/platform/runbooks.git","web_url":"https://gitlab.example.com/platform/runbooks"}`))
	})
	mux.HandleFunc("GET /api/v4/projects/77/protected_branches", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"name":"release/*"},{"name":"main"}]`))
	})
	var hookBody map[string]any
	mux.HandleFunc("POST /api/v4/projects/77/hooks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&hookBody)
		w.WriteHeader(201)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	g := &GitLab{BaseURL: srv.URL, Token: "glpat"}
	ctx := context.Background()
	info, err := g.RepoInfo(ctx, Repo{Owner: "platform", Name: "runbooks"})
	if err != nil || info.ExternalID != "77" || info.Owner != "platform" || !info.Private {
		t.Fatalf("info %+v %v", info, err)
	}
	repo := Repo{ExternalID: "77"}
	p, err := g.BranchProtection(ctx, repo, "main")
	if err != nil || !p.Protected {
		t.Fatalf("protection %+v %v", p, err)
	}
	p, _ = g.BranchProtection(ctx, repo, "release/1.2")
	if !p.Protected {
		t.Fatal("wildcard protection not matched")
	}
	p, _ = g.BranchProtection(ctx, repo, "docs")
	if p.Protected {
		t.Fatal("docs should not be protected")
	}
	if err := g.CreateWebhook(ctx, repo, "https://kmdn.example.com/hooks/gitlab/rep_1", "tok"); err != nil || hookBody["token"] != "tok" || hookBody["push_events"] != true {
		t.Fatalf("hook: %v %v", err, hookBody)
	}
	cred, _ := g.Credential(ctx, repo)
	if cred.Password != "glpat" {
		t.Fatal("credential")
	}
	body := `{"ref":"refs/heads/main","after":"def","project":{"id":77,"path_with_namespace":"platform/runbooks"}}`
	r := httptest.NewRequest("POST", "/", nil)
	r.Header.Set("X-Gitlab-Event", "Push Hook")
	r.Header.Set("X-Gitlab-Token", "tok")
	ev, err := g.ParseWebhook(r, []byte(body), "tok")
	if err != nil || ev.Type != "push" || ev.After != "def" || ev.Repo.ExternalID != "77" {
		t.Fatalf("webhook %+v %v", ev, err)
	}
	if _, err := g.ParseWebhook(r, []byte(body), "other"); !errors.Is(err, ErrBadSignature) {
		t.Fatal("bad token accepted")
	}
}

func TestSplitCloneURL(t *testing.T) {
	cases := map[string][2]string{
		"https://github.com/northwind/handbook.git": {"northwind", "handbook"},
		"git@github.com:northwind/handbook.git":     {"northwind", "handbook"},
		"file:///tmp/x/handbook":                    {"x", "handbook"},
		"https://gitlab.example.com/a/b/runbooks":   {"b", "runbooks"},
	}
	for in, want := range cases {
		o, n := SplitCloneURL(in)
		if o != want[0] || n != want[1] {
			t.Errorf("%s: %s/%s", in, o, n)
		}
	}
}

func TestPlainGitWebhook(t *testing.T) {
	g := &PlainGit{}
	r := httptest.NewRequest("POST", "/", nil)
	r.Header.Set("X-Kmdn-Token", "s")
	ev, err := g.ParseWebhook(r, []byte(`{"branch":"main","after":"x"}`), "s")
	if err != nil || ev.Type != "push" || ev.Branch != "main" {
		t.Fatalf("%+v %v", ev, err)
	}
	if _, err := g.ParseWebhook(r, nil, ""); !errors.Is(err, ErrBadSignature) {
		t.Fatal("empty secret must reject")
	}
}
