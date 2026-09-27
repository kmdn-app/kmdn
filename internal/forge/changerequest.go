package forge

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// --- GitHub

func (g *GitHubApp) pullsPath(repo Repo) string {
	return "/repos/" + url.PathEscape(repo.Owner) + "/" + url.PathEscape(repo.Name) + "/pulls"
}

// graphqlURL is api.github.com/graphql, or <host>/api/graphql on GHES
// (whose REST API lives at <host>/api/v3).
func (g *GitHubApp) graphqlURL() string {
	base := strings.TrimRight(g.APIURL, "/")
	if b, ok := strings.CutSuffix(base, "/api/v3"); ok {
		return b + "/api/graphql"
	}
	return base + "/graphql"
}

// graphql runs a query as the repo's installation. GraphQL reports failures
// in the body of a 200.
func (g *GitHubApp) graphql(ctx context.Context, repo Repo, query string, vars map[string]any) error {
	var out struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := g.asInstall(ctx, repo, http.MethodPost, g.graphqlURL(), map[string]any{"query": query, "variables": vars}, &out); err != nil {
		return err
	}
	if len(out.Errors) > 0 {
		return &APIError{Status: http.StatusUnprocessableEntity, Message: out.Errors[0].Message}
	}
	return nil
}

type ghPull struct {
	Number  int64  `json:"number"`
	NodeID  string `json:"node_id"`
	HTMLURL string `json:"html_url"`
	Draft   bool   `json:"draft"`
}

func (p ghPull) ref() ChangeRequest {
	return ChangeRequest{URL: p.HTMLURL, Ref: strconv.FormatInt(p.Number, 10), Node: p.NodeID, Draft: p.Draft}
}

// OpenChangeRequest opens a pull request from head into base. Repositories
// without draft pull requests (private ones on free plans) get a regular one.
func (g *GitHubApp) OpenChangeRequest(ctx context.Context, repo Repo, in ChangeRequestInput) (ChangeRequest, error) {
	body := map[string]any{"title": in.Title, "head": in.Head, "base": in.Base, "body": in.Body, "maintainer_can_modify": true, "draft": in.Draft}
	var pr ghPull
	err := g.asInstall(ctx, repo, http.MethodPost, g.pullsPath(repo), body, &pr)
	var ae *APIError
	if in.Draft && errors.As(err, &ae) && ae.Status == http.StatusUnprocessableEntity && strings.Contains(strings.ToLower(ae.Message), "draft") {
		body["draft"] = false
		err = g.asInstall(ctx, repo, http.MethodPost, g.pullsPath(repo), body, &pr)
	}
	return pr.ref(), err
}

// FindChangeRequest returns the open pull request from head.
func (g *GitHubApp) FindChangeRequest(ctx context.Context, repo Repo, head string) (ChangeRequest, bool, error) {
	var prs []ghPull
	q := url.Values{"head": {repo.Owner + ":" + head}, "state": {"open"}}
	if err := g.asInstall(ctx, repo, http.MethodGet, g.pullsPath(repo)+"?"+q.Encode(), nil, &prs); err != nil || len(prs) == 0 {
		return ChangeRequest{}, false, err
	}
	return prs[0].ref(), true, nil
}

// SetDraft converts the pull request to a draft or marks it ready for
// review. REST can't do either; GraphQL can.
func (g *GitHubApp) SetDraft(ctx context.Context, repo Repo, cr ChangeRequest, draft bool) error {
	node := cr.Node
	if node == "" {
		var pr ghPull
		if err := g.asInstall(ctx, repo, http.MethodGet, g.pullsPath(repo)+"/"+url.PathEscape(cr.Ref), nil, &pr); err != nil {
			return err
		}
		if pr.Draft == draft {
			return nil
		}
		node = pr.NodeID
	}
	q := `mutation($id: ID!) { markPullRequestReadyForReview(input: {pullRequestId: $id}) { pullRequest { isDraft } } }`
	if draft {
		q = `mutation($id: ID!) { convertPullRequestToDraft(input: {pullRequestId: $id}) { pullRequest { isDraft } } }`
	}
	return g.graphql(ctx, repo, q, map[string]any{"id": node})
}

// --- GitLab

// A GitLab merge request is a draft when its title starts with "Draft:"
// (older versions also accept "[Draft]", "(Draft)" and "WIP:").
var glDraftPrefixes = []string{"draft:", "[draft]", "(draft)", "wip:", "[wip]"}

func glUndraft(title string) string {
	for {
		t := strings.TrimSpace(title)
		trimmed := false
		for _, p := range glDraftPrefixes {
			if len(t) >= len(p) && strings.EqualFold(t[:len(p)], p) {
				title, trimmed = t[len(p):], true
				break
			}
		}
		if !trimmed {
			return t
		}
	}
}

func (g *GitLab) mrPath(repo Repo) string {
	return "/projects/" + projectRef(repo) + "/merge_requests"
}

// OpenChangeRequest opens a merge request from head into base.
func (g *GitLab) OpenChangeRequest(ctx context.Context, repo Repo, in ChangeRequestInput) (ChangeRequest, error) {
	title := glUndraft(in.Title)
	if in.Draft {
		title = "Draft: " + title
	}
	var mr struct {
		IID    int64  `json:"iid"`
		WebURL string `json:"web_url"`
	}
	err := g.do(ctx, http.MethodPost, g.mrPath(repo),
		map[string]any{"source_branch": in.Head, "target_branch": in.Base, "title": title, "description": in.Body, "remove_source_branch": true}, &mr)
	return ChangeRequest{URL: mr.WebURL, Ref: strconv.FormatInt(mr.IID, 10), Draft: in.Draft}, err
}

// FindChangeRequest returns the open merge request from head.
func (g *GitLab) FindChangeRequest(ctx context.Context, repo Repo, head string) (ChangeRequest, bool, error) {
	var mrs []struct {
		IID    int64  `json:"iid"`
		WebURL string `json:"web_url"`
		Draft  bool   `json:"draft"`
	}
	q := url.Values{"source_branch": {head}, "state": {"opened"}}
	if err := g.do(ctx, http.MethodGet, g.mrPath(repo)+"?"+q.Encode(), nil, &mrs); err != nil || len(mrs) == 0 {
		return ChangeRequest{}, false, err
	}
	return ChangeRequest{URL: mrs[0].WebURL, Ref: strconv.FormatInt(mrs[0].IID, 10), Draft: mrs[0].Draft}, true, nil
}

// SetDraft adds or removes the "Draft:" title prefix.
func (g *GitLab) SetDraft(ctx context.Context, repo Repo, cr ChangeRequest, draft bool) error {
	var mr struct {
		Title string `json:"title"`
	}
	p := g.mrPath(repo) + "/" + url.PathEscape(cr.Ref)
	if err := g.do(ctx, http.MethodGet, p, nil, &mr); err != nil {
		return err
	}
	title := glUndraft(mr.Title)
	if draft {
		title = "Draft: " + title
	}
	if title == mr.Title {
		return nil
	}
	return g.do(ctx, http.MethodPut, p, map[string]any{"title": title}, nil)
}
