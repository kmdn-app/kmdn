package publish

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
)

// Routes registers the publish endpoints.
func (s *Service) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.Require)
		r.Get("/revisions/{revision}/commit-preview", s.preview)
		r.Post("/revisions/{revision}/publish", s.publish)
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
	if err != nil {
		api.Error(w, r, err)
		return rev, repo, revisions.Caller{}, false
	}
	if role == access.None {
		api.Error(w, r, api.ErrNotFound)
		return rev, repo, revisions.Caller{}, false
	}
	return rev, repo, revisions.Caller{User: p.User, Role: role}, true
}

func (s *Service) preview(w http.ResponseWriter, r *http.Request) {
	rev, repo, _, ok := s.load(w, r)
	if !ok {
		return
	}
	p, err := s.Preview(r.Context(), repo, rev, r.URL.Query().Get("title"), r.URL.Query().Get("body"))
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, p)
}

func (s *Service) publish(w http.ResponseWriter, r *http.Request) {
	rev, repo, c, ok := s.load(w, r)
	if !ok {
		return
	}
	var in struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	id, err := s.Publish(r.Context(), repo, rev, c, in.Title, in.Body)
	var cf *revisions.ErrConflict
	switch {
	case errors.Is(err, revisions.ErrForbidden):
		api.Error(w, r, api.Err(http.StatusForbidden, "forbidden", "Only maintainers can publish, once every reviewer has approved."))
	case errors.As(err, &cf):
		api.Error(w, r, api.Err(http.StatusConflict, cf.Code, cf.Msg))
	case err != nil:
		api.Error(w, r, err)
	default:
		s.Jobs.Notify()
		api.JSON(w, http.StatusAccepted, map[string]any{"job_id": id})
	}
}
