package forge

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/kmdn-app/kmdn/internal/gitmirror"
)

// PlainGit is a remote reachable by URL with an optional token. It has no API:
// no protection detection, and webhooks are a simple token-authenticated POST
// ({"branch": "...", "after": "..."}) that any CI or git hook can send.
type PlainGit struct {
	Username string
	Token    string
}

func (g *PlainGit) Kind() string { return KindGit }

func (g *PlainGit) Credential(context.Context, Repo) (*gitmirror.Credential, error) {
	if g.Token == "" {
		return nil, nil
	}
	u := g.Username
	if u == "" {
		u = "git"
	}
	return &gitmirror.Credential{Username: u, Password: g.Token}, nil
}

func (g *PlainGit) RepoInfo(_ context.Context, repo Repo) (RepoInfo, error) {
	return RepoInfo{Owner: repo.Owner, Name: repo.Name, CloneURL: repo.CloneURL, WebURL: webURLFromClone(repo.CloneURL)}, nil
}

func (g *PlainGit) BranchProtection(context.Context, Repo, string) (Protection, error) {
	return Protection{Known: false, Detail: "Plain git remotes have no protection API. kmdn pushes directly."}, nil
}

func (g *PlainGit) ParseWebhook(r *http.Request, body []byte, secret string) (Event, error) {
	got := r.Header.Get("X-Kmdn-Token")
	if secret == "" || subtle.ConstantTimeCompare([]byte(got), []byte(secret)) != 1 {
		return Event{}, ErrBadSignature
	}
	var p struct {
		Branch string `json:"branch"`
		After  string `json:"after"`
	}
	_ = json.Unmarshal(body, &p)
	return Event{DeliveryID: r.Header.Get("X-Kmdn-Delivery"), Type: "push", Branch: p.Branch, After: p.After}, nil
}

// SplitCloneURL derives owner and name from a git URL (https, ssh or file).
func SplitCloneURL(u string) (owner, name string) {
	s := strings.TrimSuffix(strings.TrimRight(u, "/"), ".git")
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
		if j := strings.Index(s, "/"); j >= 0 {
			s = s[j+1:]
		}
	} else if i := strings.Index(s, ":"); i >= 0 {
		s = s[i+1:] // git@host:owner/name
	}
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if len(parts) == 1 {
		return "", parts[0]
	}
	return strings.Join(parts[len(parts)-2:len(parts)-1], "/"), parts[len(parts)-1]
}

func webURLFromClone(u string) string {
	if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
		return strings.TrimSuffix(u, ".git")
	}
	return ""
}
