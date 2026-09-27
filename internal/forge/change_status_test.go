package forge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGitHubChangeRequestStatus(t *testing.T) {
	mux := http.NewServeMux()
	githubStub(mux)
	var reply string
	mux.HandleFunc("GET /repos/northwind/handbook/pulls/7", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(reply))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	g := &GitHubApp{APIURL: srv.URL, AppID: "123", PrivateKey: testKey(t)}
	repo := Repo{Owner: "northwind", Name: "handbook", InstallID: "42"}
	cases := []struct {
		name, body string
		want       ChangeRequestStatus
	}{
		{"open", `{"state":"open","merged":false,"head":{"sha":"saved"},"auto_merge":null}`, ChangeRequestStatus{HeadSHA: "saved", Open: true}},
		{"queued", `{"state":"open","merged":false,"head":{"sha":"saved"},"auto_merge":{"merge_method":"merge"}}`, ChangeRequestStatus{HeadSHA: "saved", Open: true, Queued: true}},
		{"merged", `{"state":"closed","merged":true,"head":{"sha":"saved"},"merge_commit_sha":"landed"}`, ChangeRequestStatus{HeadSHA: "saved", MergeSHA: "landed", Merged: true}},
		{"closed", `{"state":"closed","merged":false,"head":{"sha":"saved"}}`, ChangeRequestStatus{HeadSHA: "saved", Closed: true}},
		{"unknown", `{"state":"future_state","head":{"sha":"saved"}}`, ChangeRequestStatus{HeadSHA: "saved"}},
		{"missing state", `{}`, ChangeRequestStatus{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reply = tc.body
			got, err := g.ChangeRequestStatus(context.Background(), repo, "7")
			if err != nil || got != tc.want {
				t.Fatalf("status %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func TestGitLabChangeRequestStatus(t *testing.T) {
	var reply string
	code := http.StatusOK
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v4/projects/77/merge_requests/5", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(reply))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	g := &GitLab{BaseURL: srv.URL, Token: "glpat"}
	repo := Repo{ExternalID: "77"}
	cases := []struct {
		name, body string
		want       ChangeRequestStatus
	}{
		{"open", `{"state":"opened","sha":"saved"}`, ChangeRequestStatus{HeadSHA: "saved", Open: true}},
		{"pipeline queued", `{"state":"opened","sha":"saved","merge_when_pipeline_succeeds":true}`, ChangeRequestStatus{HeadSHA: "saved", Open: true, Queued: true}},
		{"auto merge queued", `{"state":"opened","sha":"saved","auto_merge_enabled":true}`, ChangeRequestStatus{HeadSHA: "saved", Open: true, Queued: true}},
		{"merged", `{"state":"merged","sha":"saved","merge_commit_sha":"landed"}`, ChangeRequestStatus{HeadSHA: "saved", MergeSHA: "landed", Merged: true}},
		{"closed", `{"state":"closed","sha":"saved"}`, ChangeRequestStatus{HeadSHA: "saved", Closed: true}},
		// GitLab's transitional locked state is not proof that a merge stopped.
		{"locked", `{"state":"locked","sha":"saved"}`, ChangeRequestStatus{HeadSHA: "saved"}},
		{"unknown", `{"state":"future_state","sha":"saved"}`, ChangeRequestStatus{HeadSHA: "saved"}},
		{"missing state", `{}`, ChangeRequestStatus{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reply = tc.body
			got, err := g.ChangeRequestStatus(context.Background(), repo, "5")
			if err != nil || got != tc.want {
				t.Fatalf("status %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
	code, reply = http.StatusServiceUnavailable, `{"message":"temporarily unavailable"}`
	if _, err := g.ChangeRequestStatus(context.Background(), repo, "5"); err == nil {
		t.Fatal("an unavailable forge cannot confirm a merge outcome")
	}
}
