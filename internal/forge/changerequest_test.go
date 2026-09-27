package forge

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// githubStub serves an installation token for installation 42.
func githubStub(mux *http.ServeMux) {
	mux.HandleFunc("POST /app/installations/42/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_test", "expires_at": time.Now().Add(time.Hour)})
	})
}

func TestGitHubDraftPullRequest(t *testing.T) {
	mux := http.NewServeMux()
	githubStub(mux)
	var opened []map[string]any
	draftsSupported := false
	mux.HandleFunc("POST /repos/northwind/handbook/pulls", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		opened = append(opened, body)
		if body["draft"] == true && !draftsSupported {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"Draft pull requests are not supported in this repository."}`))
			return
		}
		_, _ = w.Write([]byte(`{"number":7,"node_id":"PR_7","html_url":"https://github.com/northwind/handbook/pull/7","draft":` + map[bool]string{true: "true", false: "false"}[body["draft"] == true] + `}`))
	})
	mux.HandleFunc("GET /repos/northwind/handbook/pulls", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("head") != "northwind:kmdn/3-onboarding" || r.URL.Query().Get("state") != "open" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`[{"number":7,"node_id":"PR_7","html_url":"https://github.com/northwind/handbook/pull/7","draft":true}]`))
	})
	var mutations []string
	mux.HandleFunc("POST /graphql", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token ghs_test" {
			http.Error(w, `{"message":"no"}`, http.StatusUnauthorized)
			return
		}
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Variables["id"] != "PR_7" {
			_, _ = w.Write([]byte(`{"errors":[{"message":"Could not resolve to a node"}]}`))
			return
		}
		mutations = append(mutations, body.Query)
		_, _ = w.Write([]byte(`{"data":{}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	g := &GitHubApp{APIURL: srv.URL, AppID: "123", PrivateKey: testKey(t)}
	ctx := context.Background()
	repo := Repo{Owner: "northwind", Name: "handbook", InstallID: "42"}
	in := ChangeRequestInput{Head: "kmdn/3-onboarding", Base: "main", Title: "Onboarding", Body: "b", Draft: true}

	// No drafts on this plan: a regular pull request.
	cr, err := g.OpenChangeRequest(ctx, repo, in)
	if err != nil || cr.Ref != "7" || cr.Draft || len(opened) != 2 || opened[1]["draft"] != false {
		t.Fatalf("fallback: %+v %v %v", cr, err, opened)
	}
	draftsSupported = true
	cr, err = g.OpenChangeRequest(ctx, repo, in)
	if err != nil || !cr.Draft || cr.Node != "PR_7" || cr.URL == "" {
		t.Fatalf("draft: %+v %v", cr, err)
	}
	found, ok, err := g.FindChangeRequest(ctx, repo, "kmdn/3-onboarding")
	if err != nil || !ok || found.Ref != "7" || !found.Draft {
		t.Fatalf("find: %+v %v %v", found, ok, err)
	}
	if _, ok, _ := g.FindChangeRequest(ctx, repo, "kmdn/4-other"); ok {
		t.Fatal("found a pull request for another branch")
	}
	if err := g.SetDraft(ctx, repo, cr, false); err != nil {
		t.Fatal(err)
	}
	if err := g.SetDraft(ctx, repo, cr, true); err != nil {
		t.Fatal(err)
	}
	if len(mutations) != 2 || !strings.Contains(mutations[0], "markPullRequestReadyForReview") || !strings.Contains(mutations[1], "convertPullRequestToDraft") {
		t.Fatalf("mutations: %v", mutations)
	}
	if err := g.SetDraft(ctx, repo, ChangeRequest{Ref: "7", Node: "nope"}, false); err == nil {
		t.Fatal("GraphQL errors should fail")
	}
}

func TestGitHubGraphQLURL(t *testing.T) {
	for api, want := range map[string]string{
		"https://api.github.com":          "https://api.github.com/graphql",
		"https://ghe.example.com/api/v3/": "https://ghe.example.com/api/graphql",
		"https://ghe.example.com/api/v3":  "https://ghe.example.com/api/graphql",
	} {
		if got := (&GitHubApp{APIURL: api}).graphqlURL(); got != want {
			t.Errorf("%s: %s, want %s", api, got, want)
		}
	}
}

func TestGitLabDraftMergeRequest(t *testing.T) {
	mux := http.NewServeMux()
	title := ""
	mux.HandleFunc("POST /api/v4/projects/77/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		title = body["title"].(string)
		_, _ = w.Write([]byte(`{"iid":5,"web_url":"https://gitlab.example.com/platform/runbooks/-/merge_requests/5"}`))
	})
	mux.HandleFunc("GET /api/v4/projects/77/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("source_branch") != "kmdn/3-onboarding" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`[{"iid":5,"web_url":"u","draft":true}]`))
	})
	mux.HandleFunc("GET /api/v4/projects/77/merge_requests/5", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"title": title})
	})
	puts := 0
	mux.HandleFunc("PUT /api/v4/projects/77/merge_requests/5", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		title = body["title"].(string)
		puts++
		_, _ = w.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	g := &GitLab{BaseURL: srv.URL, Token: "glpat"}
	ctx := context.Background()
	repo := Repo{ExternalID: "77"}
	cr, err := g.OpenChangeRequest(ctx, repo, ChangeRequestInput{Head: "kmdn/3-onboarding", Base: "main", Title: "Onboarding", Draft: true})
	if err != nil || cr.Ref != "5" || !cr.Draft || title != "Draft: Onboarding" {
		t.Fatalf("open: %+v %v %q", cr, err, title)
	}
	if f, ok, err := g.FindChangeRequest(ctx, repo, "kmdn/3-onboarding"); err != nil || !ok || f.Ref != "5" {
		t.Fatalf("find: %+v %v %v", f, ok, err)
	}
	if err := g.SetDraft(ctx, repo, cr, false); err != nil || title != "Onboarding" {
		t.Fatalf("ready: %v %q", err, title)
	}
	if err := g.SetDraft(ctx, repo, cr, false); err != nil || puts != 1 {
		t.Fatalf("already ready should not update: %v %d", err, puts)
	}
	if err := g.SetDraft(ctx, repo, cr, true); err != nil || title != "Draft: Onboarding" {
		t.Fatalf("draft: %v %q", err, title)
	}
	for in, want := range map[string]string{"Draft: x": "x", "[Draft] WIP: x": "x", "(draft) x": "x", "Drafting notes": "Drafting notes"} {
		if got := glUndraft(in); got != want {
			t.Errorf("glUndraft(%q) = %q, want %q", in, got, want)
		}
	}
}

func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestGitHubMerge(t *testing.T) {
	mux := http.NewServeMux()
	githubStub(mux)
	reply := `{"sha":"m3rg3","merged":true}`
	status := 200
	var got map[string]any
	mux.HandleFunc("PUT /repos/northwind/handbook/pulls/7/merge", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	})
	var auto map[string]any
	mux.HandleFunc("POST /graphql", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !strings.Contains(body.Query, "enablePullRequestAutoMerge") || !strings.Contains(body.Query, "mergeMethod: MERGE") {
			_, _ = w.Write([]byte(`{"errors":[{"message":"unexpected"}]}`))
			return
		}
		auto = body.Variables
		_, _ = w.Write([]byte(`{"data":{}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	g := &GitHubApp{APIURL: srv.URL, AppID: "123", PrivateKey: testKey(t)}
	ctx := context.Background()
	repo := Repo{Owner: "northwind", Name: "handbook", InstallID: "42"}
	cr := ChangeRequest{Ref: "7", Node: "PR_7"}
	in := MergeInput{Title: "Refresh", Body: "Kmdn-Revision: x", HeadSHA: "t1p"}

	res, err := g.MergeChangeRequest(ctx, repo, cr, in)
	if err != nil || res.SHA != "m3rg3" || res.Queued || got["merge_method"] != "merge" || got["sha"] != "t1p" || got["commit_title"] != "Refresh" {
		t.Fatalf("merge: %+v %v %v", res, err, got)
	}
	// Required checks still running: auto-merge.
	status, reply = 405, `{"message":"Required status check \"ci\" is expected."}`
	res, err = g.MergeChangeRequest(ctx, repo, cr, in)
	if err != nil || !res.Queued || auto["id"] != "PR_7" || auto["head"] != "t1p" || auto["headline"] != "Refresh" {
		t.Fatalf("auto-merge: %+v %v %v", res, err, auto)
	}
	for msg, want := range map[string]error{
		"Merge commits are not allowed on this repository.":                       ErrMergeCommitsDisabled,
		"At least 1 approving review is required by reviewers with write access.": ErrApprovalsRequired,
		"Pull Request is not mergeable":                                           ErrNotMergeable,
		"Base branch was modified. Review and try the merge again.":               ErrHeadChanged,
	} {
		reply = `{"message":` + strconv.Quote(msg) + `}`
		if _, err := g.MergeChangeRequest(ctx, repo, cr, in); !errors.Is(err, want) {
			t.Errorf("%q: %v, want %v", msg, err, want)
		}
	}
}

func TestGitLabMerge(t *testing.T) {
	mux := http.NewServeMux()
	detailed := "mergeable"
	var calls []map[string]any
	mux.HandleFunc("PUT /api/v4/projects/77/merge_requests/5/merge", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls = append(calls, body)
		switch {
		case detailed == "mergeable":
			_, _ = w.Write([]byte(`{"state":"merged","merge_commit_sha":"m3rg3"}`))
		case body["merge_when_pipeline_succeeds"] == true:
			_, _ = w.Write([]byte(`{"state":"opened","merge_when_pipeline_succeeds":true}`))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
			_, _ = w.Write([]byte(`{"message":"405 Method Not Allowed"}`))
		}
	})
	mux.HandleFunc("GET /api/v4/projects/77/merge_requests/5", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"detailed_merge_status": detailed})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	g := &GitLab{BaseURL: srv.URL, Token: "glpat"}
	ctx := context.Background()
	repo, cr := Repo{ExternalID: "77"}, ChangeRequest{Ref: "5"}
	in := MergeInput{Title: "Refresh", Body: "Kmdn-Revision: x", HeadSHA: "t1p"}
	res, err := g.MergeChangeRequest(ctx, repo, cr, in)
	if err != nil || res.SHA != "m3rg3" || calls[0]["squash"] != false || calls[0]["sha"] != "t1p" || calls[0]["merge_commit_message"] != "Refresh\n\nKmdn-Revision: x" {
		t.Fatalf("merge: %+v %v %v", res, err, calls)
	}
	detailed = "ci_still_running"
	if res, err := g.MergeChangeRequest(ctx, repo, cr, in); err != nil || !res.Queued {
		t.Fatalf("pipeline running: %+v %v", res, err)
	}
	detailed = "not_approved"
	if _, err := g.MergeChangeRequest(ctx, repo, cr, in); !errors.Is(err, ErrApprovalsRequired) {
		t.Fatalf("not approved: %v", err)
	}
}
