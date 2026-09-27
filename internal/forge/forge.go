// Package forge integrates kmdn with code hosts: GitHub (App), GitLab (OAuth
// app + access tokens) and plain git remotes (development and self-hosted git).
// See docs/specs/06-git-and-forges.md#forge-adapters.
package forge

import (
	"context"
	"errors"
	"net/http"

	"github.com/kmdn-app/kmdn/internal/gitmirror"
)

// Kinds.
const (
	KindGitHub = "github"
	KindGitLab = "gitlab"
	KindGit    = "git"
)

// Host is a configured forge (one GitHub App, one GitLab instance, or the
// generic git host).
type Host struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	BaseURL     string `json:"base_url"` // web URL, e.g. https://github.com or https://gitlab.example.com
	APIURL      string `json:"api_url"`  // e.g. https://api.github.com
	DisplayName string `json:"display_name"`
	AppID       string `json:"app_id,omitempty"`
	AppSlug     string `json:"app_slug,omitempty"`
	ClientID    string `json:"client_id,omitempty"`
}

// Repo identifies a repository on a host.
type Repo struct {
	Owner      string // org/user (GitHub) or namespace path (GitLab)
	Name       string
	ExternalID string // numeric id on the forge
	InstallID  string // GitHub installation id
	CloneURL   string // for kind "git"
}

// RepoInfo describes a repository as reported by the forge.
type RepoInfo struct {
	ExternalID    string `json:"external_id"`
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	CloneURL      string `json:"clone_url"`
	WebURL        string `json:"web_url"`
	UpdatedAt     string `json:"updated_at,omitempty"`
}

// Protection summarizes branch protection on the target branch.
type Protection struct {
	Protected       bool   `json:"protected"`
	RequiredReviews int    `json:"required_reviews"`
	RequiredChecks  int    `json:"required_checks"`
	Detail          string `json:"detail"`
	Known           bool   `json:"known"` // false when the forge didn't let us read it
}

// Event is a normalized webhook event.
type Event struct {
	DeliveryID string
	Type       string // push | installation | change_request | ping | other
	Repo       Repo
	Branch     string
	After      string // new head sha (push)
	Install    *Install
	Removed    bool // installation removed / repo access revoked
	// ChangeRequest is set for pull/merge request events (Type change_request).
	ChangeRequest *ChangeRequestEvent
}

// ChangeRequest is a pull request (GitHub) or merge request (GitLab) kmdn opened.
type ChangeRequest struct {
	URL   string `json:"url"`
	Ref   string `json:"ref"`  // PR number / MR iid
	Node  string `json:"node"` // GitHub GraphQL id (draft ↔ ready go through GraphQL)
	Draft bool   `json:"draft"`
}

// ChangeRequestEvent says a pull/merge request was merged or closed.
type ChangeRequestEvent struct {
	Ref      string // number / iid
	Head     string // source branch
	Merged   bool
	Closed   bool // closed without merging
	MergeSHA string
}

// ChangeRequestInput opens a pull/merge request from Head into Base.
type ChangeRequestInput struct {
	Head, Base  string
	Title, Body string
	// Draft opens it as a draft. Forges or plans without drafts open a
	// regular one; ChangeRequest.Draft says which it is.
	Draft bool
}

// ChangeRequester opens and updates the pull/merge request of a revision
// branch (docs/specs/06-git-and-forges.md#revision-branches).
type ChangeRequester interface {
	OpenChangeRequest(ctx context.Context, repo Repo, in ChangeRequestInput) (ChangeRequest, error)
	// FindChangeRequest returns the open change request from head, if any
	// (a retry after kmdn opened one but didn't record it).
	FindChangeRequest(ctx context.Context, repo Repo, head string) (ChangeRequest, bool, error)
	// SetDraft turns the change request into a draft, or marks it ready for review.
	SetDraft(ctx context.Context, repo Repo, cr ChangeRequest, draft bool) error
}

// Install is a GitHub App installation.
type Install struct {
	ExternalID   string `json:"external_id"`
	AccountLogin string `json:"account_login"`
}

// ErrNotFound means the forge has no such repository (or no access).
var ErrNotFound = errors.New("forge: repository not found or not accessible")

// ErrBadSignature means a webhook failed verification.
var ErrBadSignature = errors.New("forge: webhook signature invalid")

// Adapter is implemented per forge kind.
type Adapter interface {
	Kind() string
	// Credential returns git credentials for clone/fetch/push of repo.
	Credential(ctx context.Context, repo Repo) (*gitmirror.Credential, error)
	// RepoInfo fetches repository metadata.
	RepoInfo(ctx context.Context, repo Repo) (RepoInfo, error)
	// BranchProtection reports whether pushes to branch are restricted.
	BranchProtection(ctx context.Context, repo Repo, branch string) (Protection, error)
	// ParseWebhook verifies and normalizes a webhook request. secret is the
	// shared secret for this host or repo.
	ParseWebhook(r *http.Request, body []byte, secret string) (Event, error)
}
