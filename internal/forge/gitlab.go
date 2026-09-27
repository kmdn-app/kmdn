package forge

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/kmdn-app/kmdn/internal/gitmirror"
)

// GitLab talks to gitlab.com or self-managed GitLab with a project or group
// access token.
type GitLab struct {
	BaseURL string // https://gitlab.com
	Token   string
	HTTP    *http.Client
}

func (g *GitLab) Kind() string { return KindGitLab }

func (g *GitLab) client() *http.Client {
	if g.HTTP != nil {
		return g.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (g *GitLab) do(ctx context.Context, method, p string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(g.BaseURL, "/")+"/api/v4"+p, rd)
	if err != nil {
		return err
	}
	req.Header.Set("PRIVATE-TOKEN", g.Token)
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
		if res.StatusCode == http.StatusNotFound {
			return fmt.Errorf("%w (%s)", ErrNotFound, p)
		}
		var e struct {
			Message any `json:"message"`
		}
		_ = json.Unmarshal(b, &e)
		return &APIError{Status: res.StatusCode, Message: fmt.Sprint(e.Message)}
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

func projectRef(repo Repo) string {
	if repo.ExternalID != "" {
		return repo.ExternalID
	}
	return url.PathEscape(strings.Trim(repo.Owner+"/"+repo.Name, "/"))
}

func (g *GitLab) Credential(context.Context, Repo) (*gitmirror.Credential, error) {
	return &gitmirror.Credential{Username: "oauth2", Password: g.Token}, nil
}

type glProject struct {
	ID                int64  `json:"id"`
	Path              string `json:"path"`
	PathWithNamespace string `json:"path_with_namespace"`
	DefaultBranch     string `json:"default_branch"`
	Visibility        string `json:"visibility"`
	HTTPURLToRepo     string `json:"http_url_to_repo"`
	WebURL            string `json:"web_url"`
	LastActivityAt    string `json:"last_activity_at"`
}

func (g *GitLab) RepoInfo(ctx context.Context, repo Repo) (RepoInfo, error) {
	var p glProject
	if err := g.do(ctx, http.MethodGet, "/projects/"+projectRef(repo), nil, &p); err != nil {
		return RepoInfo{}, err
	}
	owner := path.Dir(p.PathWithNamespace)
	return RepoInfo{ExternalID: strconv.FormatInt(p.ID, 10), Owner: owner, Name: p.Path, DefaultBranch: p.DefaultBranch,
		Private: p.Visibility != "public", CloneURL: p.HTTPURLToRepo, WebURL: p.WebURL, UpdatedAt: p.LastActivityAt}, nil
}

func (g *GitLab) BranchProtection(ctx context.Context, repo Repo, branch string) (Protection, error) {
	var list []struct {
		Name             string `json:"name"`
		PushAccessLevels []struct {
			AccessLevel int `json:"access_level"`
		} `json:"push_access_levels"`
	}
	if err := g.do(ctx, http.MethodGet, "/projects/"+projectRef(repo)+"/protected_branches?per_page=100", nil, &list); err != nil {
		return Protection{}, err
	}
	p := Protection{Known: true}
	for _, pb := range list {
		if ok, _ := path.Match(pb.Name, branch); ok || pb.Name == branch {
			p.Protected = true
		}
	}
	if p.Protected {
		p.Detail = "Protected: kmdn will open a merge request on publish."
	} else {
		p.Detail = "Not protected: kmdn publishes directly."
	}
	return p, nil
}

// CreateWebhook registers a project webhook for push and merge request events.
func (g *GitLab) CreateWebhook(ctx context.Context, repo Repo, hookURL, secret string) error {
	body := map[string]any{"url": hookURL, "token": secret, "push_events": true, "merge_requests_events": true, "enable_ssl_verification": strings.HasPrefix(hookURL, "https://")}
	return g.do(ctx, http.MethodPost, "/projects/"+projectRef(repo)+"/hooks", body, nil)
}

func (g *GitLab) ParseWebhook(r *http.Request, body []byte, secret string) (Event, error) {
	got := r.Header.Get("X-Gitlab-Token")
	if secret == "" || subtle.ConstantTimeCompare([]byte(got), []byte(secret)) != 1 {
		return Event{}, ErrBadSignature
	}
	ev := Event{DeliveryID: r.Header.Get("X-Gitlab-Event-UUID"), Type: "other"}
	var p struct {
		Ref     string `json:"ref"`
		After   string `json:"after"`
		Project struct {
			ID                int64  `json:"id"`
			PathWithNamespace string `json:"path_with_namespace"`
		} `json:"project"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return ev, err
	}
	ev.Repo = Repo{Owner: path.Dir(p.Project.PathWithNamespace), Name: path.Base(p.Project.PathWithNamespace), ExternalID: strconv.FormatInt(p.Project.ID, 10)}
	if r.Header.Get("X-Gitlab-Event") == "Push Hook" {
		ev.Type = "push"
		ev.Branch = strings.TrimPrefix(p.Ref, "refs/heads/")
		ev.After = p.After
	}
	return ev, nil
}
