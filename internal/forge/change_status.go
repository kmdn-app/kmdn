package forge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

func (g *GitHubApp) ChangeRequestStatus(ctx context.Context, repo Repo, ref string) (ChangeRequestStatus, error) {
	var pr struct {
		State     string          `json:"state"`
		Merged    bool            `json:"merged"`
		MergeSHA  string          `json:"merge_commit_sha"`
		AutoMerge json.RawMessage `json:"auto_merge"`
		Head      struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	err := g.asInstall(ctx, repo, http.MethodGet, g.pullsPath(repo)+"/"+url.PathEscape(ref), nil, &pr)
	return ChangeRequestStatus{HeadSHA: pr.Head.SHA, MergeSHA: pr.MergeSHA, Open: pr.State == "open", Closed: pr.State == "closed" && !pr.Merged, Merged: pr.Merged, Queued: len(pr.AutoMerge) != 0 && string(pr.AutoMerge) != "null"}, err
}

func (g *GitLab) ChangeRequestStatus(ctx context.Context, repo Repo, ref string) (ChangeRequestStatus, error) {
	var mr struct {
		State     string `json:"state"`
		SHA       string `json:"sha"`
		MergeSHA  string `json:"merge_commit_sha"`
		Queued    bool   `json:"merge_when_pipeline_succeeds"`
		AutoMerge bool   `json:"auto_merge_enabled"`
	}
	err := g.do(ctx, http.MethodGet, g.mrPath(repo)+"/"+url.PathEscape(ref), nil, &mr)
	return ChangeRequestStatus{HeadSHA: mr.SHA, MergeSHA: mr.MergeSHA, Open: mr.State == "opened", Closed: mr.State == "closed", Merged: mr.State == "merged", Queued: mr.Queued || mr.AutoMerge}, err
}
