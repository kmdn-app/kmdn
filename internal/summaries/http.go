package summaries

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
)

// Routes registers the review summary and change summary endpoints.
func (s *Service) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.Require)
		r.Get("/revisions/{revision}/review-summary", s.get)
		r.Post("/revisions/{revision}/review-summary", s.request)
		r.Get("/repos/{repo}/change-summaries", s.changes)
	})
}

func (s *Service) load(w http.ResponseWriter, r *http.Request) (revisions.Revision, repos.Repo, revisions.Caller, bool) {
	p, _ := auth.FromContext(r.Context())
	rev, err := revisions.Get(r.Context(), s.DB, chi.URLParam(r, "revision"))
	if err != nil {
		api.Error(w, r, api.ErrNotFound)
		return rev, repos.Repo{}, revisions.Caller{}, false
	}
	repo, err := repos.Get(r.Context(), s.DB, rev.RepoID)
	if err != nil {
		api.Error(w, r, err)
		return rev, repo, revisions.Caller{}, false
	}
	role, err := access.Effective(r.Context(), s.DB, p.User, repo.ID)
	if err != nil || role == access.None {
		api.Error(w, r, api.ErrNotFound)
		return rev, repo, revisions.Caller{}, false
	}
	return rev, repo, revisions.Caller{User: p.User, Role: role}, true
}

// get returns the checks (always fresh) and the assistant's summary, if any.
func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	rev, repo, _, ok := s.load(w, r)
	if !ok {
		return
	}
	checks, err := s.Checks(r.Context(), repo, rev)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	review, err := s.GetReview(r.Context(), rev)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"checks": checks, "review": review, "pending": s.Pending(r.Context(), rev.ID), "available": s.LLM.EnabledForRepo(r.Context(), rev.RepoID)})
}

// request asks for a fresh summary (editors before submitting, reviewers any time).
func (s *Service) request(w http.ResponseWriter, r *http.Request) {
	rev, _, c, ok := s.load(w, r)
	if !ok {
		return
	}
	acc, err := s.Revisions.AccessFor(r.Context(), rev, c)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	if !acc.Member && !acc.Reviewer && !c.Role.AtLeast(access.Maintainer) {
		api.Error(w, r, api.Err(http.StatusForbidden, "forbidden", "Editors and reviewers of the revision can ask for a summary."))
		return
	}
	if !s.LLM.EnabledForRepo(r.Context(), rev.RepoID) {
		api.Error(w, r, api.Err(http.StatusConflict, "assistant_off", "The assistant isn't set up for this organization."))
		return
	}
	s.RequestReview(r.Context(), rev.ID, c.User.ID)
	w.WriteHeader(http.StatusAccepted)
}

func (s *Service) changes(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	repo, err := repos.Get(r.Context(), s.DB, chi.URLParam(r, "repo"))
	if err != nil {
		api.Error(w, r, api.ErrNotFound)
		return
	}
	if role, err := access.Effective(r.Context(), s.DB, p.User, repo.ID); err != nil || role == access.None {
		api.Error(w, r, api.ErrNotFound)
		return
	}
	out, err := ChangeSummaries(r.Context(), s.DB, repo.ID, r.URL.Query().Get("path"))
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"by_commit": out})
}
