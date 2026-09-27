package forge

import (
	"bytes"
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kmdn-app/kmdn/internal/gitmirror"
)

// GitHubApp talks to github.com or GitHub Enterprise Server as a GitHub App.
type GitHubApp struct {
	APIURL     string // https://api.github.com or https://ghes.example.com/api/v3
	AppID      string
	PrivateKey *rsa.PrivateKey
	HTTP       *http.Client

	mu     sync.Mutex
	tokens map[string]cachedToken
	now    func() time.Time
}

type cachedToken struct {
	token   string
	expires time.Time
}

// ParsePrivateKey decodes the App's PEM private key (PKCS#1 or PKCS#8).
func ParsePrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("forge: private key is not PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("forge: private key: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("forge: private key is not RSA")
	}
	return rk, nil
}

// GitHubAPIURL returns the REST base for a GitHub web URL.
func GitHubAPIURL(baseURL string) string {
	b := strings.TrimRight(baseURL, "/")
	if b == "" || b == "https://github.com" {
		return "https://api.github.com"
	}
	return b + "/api/v3"
}

func (g *GitHubApp) Kind() string { return KindGitHub }

func (g *GitHubApp) clock() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

func (g *GitHubApp) client() *http.Client {
	if g.HTTP != nil {
		return g.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// AppJWT signs a short-lived RS256 JWT identifying the App.
func (g *GitHubApp) AppJWT() (string, error) {
	now := g.clock()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{"iat": now.Add(-60 * time.Second).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": g.AppID})
	signing := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	h := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, g.PrivateKey, crypto.SHA256, h[:])
	if err != nil {
		return "", err
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// APIError is a non-2xx response.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("forge api: %d %s", e.Status, e.Message) }

func (g *GitHubApp) do(ctx context.Context, method, path, auth string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(g.APIURL, "/")+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := g.client().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(b, &e)
		if res.StatusCode == http.StatusNotFound {
			return fmt.Errorf("%w (%s)", ErrNotFound, path)
		}
		return &APIError{Status: res.StatusCode, Message: e.Message}
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

func (g *GitHubApp) asApp(ctx context.Context, method, path string, body, out any) error {
	jwt, err := g.AppJWT()
	if err != nil {
		return err
	}
	return g.do(ctx, method, path, "Bearer "+jwt, body, out)
}

// InstallationToken returns a cached installation access token.
func (g *GitHubApp) InstallationToken(ctx context.Context, installID string) (string, error) {
	g.mu.Lock()
	if t, ok := g.tokens[installID]; ok && g.clock().Before(t.expires.Add(-10*time.Minute)) {
		g.mu.Unlock()
		return t.token, nil
	}
	g.mu.Unlock()
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := g.asApp(ctx, http.MethodPost, "/app/installations/"+url.PathEscape(installID)+"/access_tokens", nil, &out); err != nil {
		return "", err
	}
	g.mu.Lock()
	if g.tokens == nil {
		g.tokens = map[string]cachedToken{}
	}
	g.tokens[installID] = cachedToken{token: out.Token, expires: out.ExpiresAt}
	g.mu.Unlock()
	return out.Token, nil
}

func (g *GitHubApp) installFor(ctx context.Context, repo Repo) (string, error) {
	if repo.InstallID != "" {
		return repo.InstallID, nil
	}
	var inst struct {
		ID int64 `json:"id"`
	}
	if err := g.asApp(ctx, http.MethodGet, "/repos/"+url.PathEscape(repo.Owner)+"/"+url.PathEscape(repo.Name)+"/installation", nil, &inst); err != nil {
		return "", err
	}
	return strconv.FormatInt(inst.ID, 10), nil
}

// RepoInstallation returns the App installation that can access repo.
func (g *GitHubApp) RepoInstallation(ctx context.Context, repo Repo) (Install, error) {
	var inst struct {
		ID      int64 `json:"id"`
		Account struct {
			Login string `json:"login"`
		} `json:"account"`
	}
	if err := g.asApp(ctx, http.MethodGet, "/repos/"+url.PathEscape(repo.Owner)+"/"+url.PathEscape(repo.Name)+"/installation", nil, &inst); err != nil {
		return Install{}, err
	}
	return Install{ExternalID: strconv.FormatInt(inst.ID, 10), AccountLogin: inst.Account.Login}, nil
}

func (g *GitHubApp) asInstall(ctx context.Context, repo Repo, method, path string, body, out any) error {
	id, err := g.installFor(ctx, repo)
	if err != nil {
		return err
	}
	tok, err := g.InstallationToken(ctx, id)
	if err != nil {
		return err
	}
	return g.do(ctx, method, path, "token "+tok, body, out)
}

func (g *GitHubApp) Credential(ctx context.Context, repo Repo) (*gitmirror.Credential, error) {
	id, err := g.installFor(ctx, repo)
	if err != nil {
		return nil, err
	}
	tok, err := g.InstallationToken(ctx, id)
	if err != nil {
		return nil, err
	}
	return &gitmirror.Credential{Username: "x-access-token", Password: tok}, nil
}

type ghRepo struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
	CloneURL      string `json:"clone_url"`
	HTMLURL       string `json:"html_url"`
	UpdatedAt     string `json:"updated_at"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
}

func (r ghRepo) info() RepoInfo {
	return RepoInfo{ExternalID: strconv.FormatInt(r.ID, 10), Owner: r.Owner.Login, Name: r.Name, DefaultBranch: r.DefaultBranch,
		Private: r.Private, CloneURL: r.CloneURL, WebURL: r.HTMLURL, UpdatedAt: r.UpdatedAt}
}

func (g *GitHubApp) RepoInfo(ctx context.Context, repo Repo) (RepoInfo, error) {
	var r ghRepo
	if err := g.asInstall(ctx, repo, http.MethodGet, "/repos/"+url.PathEscape(repo.Owner)+"/"+url.PathEscape(repo.Name), nil, &r); err != nil {
		return RepoInfo{}, err
	}
	return r.info(), nil
}

// Installations lists the App's installations.
func (g *GitHubApp) Installations(ctx context.Context) ([]Install, error) {
	var list []struct {
		ID      int64 `json:"id"`
		Account struct {
			Login string `json:"login"`
		} `json:"account"`
	}
	if err := g.asApp(ctx, http.MethodGet, "/app/installations?per_page=100", nil, &list); err != nil {
		return nil, err
	}
	out := make([]Install, len(list))
	for i, l := range list {
		out[i] = Install{ExternalID: strconv.FormatInt(l.ID, 10), AccountLogin: l.Account.Login}
	}
	return out, nil
}

// InstallationRepos lists repositories an installation can access.
func (g *GitHubApp) InstallationRepos(ctx context.Context, installID string) ([]RepoInfo, error) {
	tok, err := g.InstallationToken(ctx, installID)
	if err != nil {
		return nil, err
	}
	var out []RepoInfo
	for page := 1; page <= 20; page++ {
		var res struct {
			Repositories []ghRepo `json:"repositories"`
		}
		if err := g.do(ctx, http.MethodGet, fmt.Sprintf("/installation/repositories?per_page=100&page=%d", page), "token "+tok, nil, &res); err != nil {
			return nil, err
		}
		for _, r := range res.Repositories {
			out = append(out, r.info())
		}
		if len(res.Repositories) < 100 {
			break
		}
	}
	return out, nil
}

func (g *GitHubApp) BranchProtection(ctx context.Context, repo Repo, branch string) (Protection, error) {
	base := "/repos/" + url.PathEscape(repo.Owner) + "/" + url.PathEscape(repo.Name)
	var b struct {
		Protected bool `json:"protected"`
	}
	if err := g.asInstall(ctx, repo, http.MethodGet, base+"/branches/"+url.PathEscape(branch), nil, &b); err != nil {
		return Protection{}, err
	}
	p := Protection{Protected: b.Protected, Known: true}
	// Rulesets are readable with metadata permission.
	var rules []struct {
		Type       string          `json:"type"`
		Parameters json.RawMessage `json:"parameters"`
	}
	if err := g.asInstall(ctx, repo, http.MethodGet, base+"/rules/branches/"+url.PathEscape(branch), nil, &rules); err == nil {
		for _, r := range rules {
			switch r.Type {
			case "pull_request":
				p.Protected = true
				var pp struct {
					Count int `json:"required_approving_review_count"`
				}
				_ = json.Unmarshal(r.Parameters, &pp)
				p.RequiredReviews = max(p.RequiredReviews, pp.Count)
			case "required_status_checks":
				p.Protected = true
				var pp struct {
					Checks []json.RawMessage `json:"required_status_checks"`
				}
				_ = json.Unmarshal(r.Parameters, &pp)
				p.RequiredChecks = max(p.RequiredChecks, len(pp.Checks))
			case "update", "non_fast_forward", "required_signatures", "required_linear_history":
				p.Protected = true
			}
		}
	}
	// Classic protection details need administration:read; best effort.
	if b.Protected {
		var cp struct {
			Reviews *struct {
				Count int `json:"required_approving_review_count"`
			} `json:"required_pull_request_reviews"`
			Checks *struct {
				Contexts []string `json:"contexts"`
			} `json:"required_status_checks"`
		}
		if err := g.asInstall(ctx, repo, http.MethodGet, base+"/branches/"+url.PathEscape(branch)+"/protection", nil, &cp); err == nil {
			if cp.Reviews != nil {
				p.RequiredReviews = max(p.RequiredReviews, cp.Reviews.Count)
			}
			if cp.Checks != nil {
				p.RequiredChecks = max(p.RequiredChecks, len(cp.Checks.Contexts))
			}
		}
	}
	p.Detail = protectionDetail(p)
	return p, nil
}

func protectionDetail(p Protection) string {
	if !p.Protected {
		return "Not protected: kmdn publishes directly."
	}
	parts := []string{}
	if p.RequiredReviews > 0 {
		parts = append(parts, fmt.Sprintf("%d required review(s)", p.RequiredReviews))
	}
	if p.RequiredChecks > 0 {
		parts = append(parts, fmt.Sprintf("%d status check(s)", p.RequiredChecks))
	}
	d := "Protected: kmdn will open a pull request on publish."
	if len(parts) > 0 {
		d += " Requires " + strings.Join(parts, " and ") + "."
	}
	return d
}

// VerifyGitHubSignature checks X-Hub-Signature-256.
func VerifyGitHubSignature(body []byte, header, secret string) bool {
	if secret == "" || !strings.HasPrefix(header, "sha256=") {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(header))
}

func (g *GitHubApp) ParseWebhook(r *http.Request, body []byte, secret string) (Event, error) {
	if !VerifyGitHubSignature(body, r.Header.Get("X-Hub-Signature-256"), secret) {
		return Event{}, ErrBadSignature
	}
	ev := Event{DeliveryID: r.Header.Get("X-GitHub-Delivery"), Type: "other"}
	var p struct {
		Action     string `json:"action"`
		Ref        string `json:"ref"`
		After      string `json:"after"`
		Repository *struct {
			ID    int64  `json:"id"`
			Name  string `json:"name"`
			Owner struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repository"`
		Installation *struct {
			ID      int64 `json:"id"`
			Account *struct {
				Login string `json:"login"`
			} `json:"account"`
		} `json:"installation"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return ev, err
	}
	if p.Repository != nil {
		ev.Repo = Repo{Owner: p.Repository.Owner.Login, Name: p.Repository.Name, ExternalID: strconv.FormatInt(p.Repository.ID, 10)}
	}
	if p.Installation != nil {
		ev.Repo.InstallID = strconv.FormatInt(p.Installation.ID, 10)
	}
	switch r.Header.Get("X-GitHub-Event") {
	case "ping":
		ev.Type = "ping"
	case "push":
		ev.Type = "push"
		ev.Branch = strings.TrimPrefix(p.Ref, "refs/heads/")
		ev.After = p.After
	case "installation", "installation_repositories":
		ev.Type = "installation"
		if p.Installation != nil {
			in := &Install{ExternalID: strconv.FormatInt(p.Installation.ID, 10)}
			if p.Installation.Account != nil {
				in.AccountLogin = p.Installation.Account.Login
			}
			ev.Install = in
		}
		ev.Removed = p.Action == "deleted"
	case "repository":
		ev.Type = "repository"
		ev.Removed = p.Action == "deleted"
	}
	return ev, nil
}

// ManifestConversion is the result of the GitHub App manifest flow.
type ManifestConversion struct {
	ID            int64  `json:"id"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	HTMLURL       string `json:"html_url"`
	PEM           string `json:"pem"`
	WebhookSecret string `json:"webhook_secret"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
}

// ConvertManifest exchanges the manifest flow code for App credentials.
func ConvertManifest(ctx context.Context, client *http.Client, apiURL, code string) (ManifestConversion, error) {
	g := &GitHubApp{APIURL: apiURL, HTTP: client}
	var out ManifestConversion
	err := g.do(ctx, http.MethodPost, "/app-manifests/"+url.PathEscape(code)+"/conversions", "", nil, &out)
	return out, err
}

// AppManifest builds the manifest kmdn asks GitHub to create.
func AppManifest(baseURL, name string) map[string]any {
	b := strings.TrimRight(baseURL, "/")
	return map[string]any{
		"name":         name,
		"url":          b,
		"redirect_url": b + "/api/v1/admin/forges/github/callback",
		"callback_urls": []string{
			b + "/api/v1/auth/oauth/github/callback",
		},
		"setup_url":       b + "/admin/repositories",
		"public":          false,
		"hook_attributes": map[string]any{"url": b + "/hooks/github", "active": true},
		"default_permissions": map[string]string{
			"contents":       "write",
			"metadata":       "read",
			"pull_requests":  "write",
			"administration": "read",
			"emails":         "read",
		},
		"default_events": []string{"push", "pull_request", "repository"},
	}
}
