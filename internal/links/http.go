package links

import (
	"errors"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
)

// Routes registers link endpoints. apply performs rewrites (collab.Hub).
func (s *Service) Routes(r chi.Router, apply Applier) {
	r.Group(func(r chi.Router) {
		r.Use(auth.Require)
		r.Get("/repos/{repo}/links/*", s.publishedLinks)
		r.Get("/repos/{repo}/graph", s.graph)
		r.Get("/revisions/{revision}/links/*", s.revisionLinks)
		r.Get("/revisions/{revision}/checks", s.checks)
		r.Post("/revisions/{revision}/files/rename-preview", s.renamePreview)
		r.Post("/revisions/{revision}/links/rewrite", func(w http.ResponseWriter, r *http.Request) { s.rewrite(w, r, apply) })
	})
}

func wildcard(r *http.Request) string {
	p := chi.URLParam(r, "*")
	if u, err := url.PathUnescape(p); err == nil {
		p = u
	}
	return strings.TrimPrefix(path.Clean("/"+p), "/")
}

func (s *Service) role(w http.ResponseWriter, r *http.Request, repoID string) (repos.Repo, revisions.Caller, bool) {
	p, _ := auth.FromContext(r.Context())
	repo, err := repos.Get(r.Context(), s.DB, repoID)
	if err != nil {
		api.Error(w, r, api.ErrNotFound)
		return repo, revisions.Caller{}, false
	}
	role, err := access.Effective(r.Context(), s.DB, p.User, repo.ID)
	if err != nil {
		api.Error(w, r, err)
		return repo, revisions.Caller{}, false
	}
	if role == access.None {
		api.Error(w, r, api.ErrNotFound)
		return repo, revisions.Caller{}, false
	}
	return repo, revisions.Caller{User: p.User, Role: role}, true
}

func (s *Service) loadRev(w http.ResponseWriter, r *http.Request) (revisions.Revision, repos.Repo, revisions.Caller, bool) {
	rev, err := revisions.Get(r.Context(), s.DB, chi.URLParam(r, "revision"))
	if err != nil {
		api.Error(w, r, api.ErrNotFound)
		return rev, repos.Repo{}, revisions.Caller{}, false
	}
	repo, c, ok := s.role(w, r, rev.RepoID)
	return rev, repo, c, ok
}

func fail(w http.ResponseWriter, r *http.Request, err error) {
	var cf *revisions.ErrConflict
	switch {
	case notFound(err):
		api.Error(w, r, api.ErrNotFound)
	case errors.As(err, &cf):
		api.Error(w, r, api.Err(http.StatusConflict, cf.Code, cf.Msg))
	case errors.Is(err, revisions.ErrForbidden):
		api.Error(w, r, api.Err(http.StatusForbidden, "forbidden", "You can't edit this revision right now."))
	default:
		api.Error(w, r, err)
	}
}

func (s *Service) publishedLinks(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := s.role(w, r, chi.URLParam(r, "repo"))
	if !ok {
		return
	}
	p := wildcard(r)
	if !repos.IsMarkdown(p) {
		api.Error(w, r, api.ErrNotFound)
		return
	}
	out, err := s.ForPublished(r.Context(), repo, p)
	if err != nil {
		fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, out)
}

func (s *Service) revisionLinks(w http.ResponseWriter, r *http.Request) {
	rev, repo, _, ok := s.loadRev(w, r)
	if !ok {
		return
	}
	p := wildcard(r)
	if !repos.IsMarkdown(p) || !repo.Scope().Contains(p) {
		api.Error(w, r, api.ErrNotFound)
		return
	}
	out, err := s.ForRevision(r.Context(), repo, rev, p)
	if err != nil {
		fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, out)
}

func (s *Service) checks(w http.ResponseWriter, r *http.Request) {
	rev, repo, _, ok := s.loadRev(w, r)
	if !ok {
		return
	}
	out, err := s.Check(r.Context(), repo, rev)
	if err != nil {
		fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, out)
}

type moveInput struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func (m moveInput) clean() (string, string) {
	c := func(p string) string { return strings.TrimPrefix(path.Clean("/"+strings.TrimSpace(p)), "/") }
	return c(m.From), c(m.To)
}

func (s *Service) renamePreview(w http.ResponseWriter, r *http.Request) {
	rev, repo, _, ok := s.loadRev(w, r)
	if !ok {
		return
	}
	var in moveInput
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	from, to := in.clean()
	list, err := s.RenamePreview(r.Context(), repo, rev, from, to)
	if err != nil {
		fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *Service) rewrite(w http.ResponseWriter, r *http.Request, apply Applier) {
	rev, repo, c, ok := s.loadRev(w, r)
	if !ok {
		return
	}
	acc, err := s.revisions().AccessFor(r.Context(), rev, c)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	if !acc.CanEdit {
		api.Error(w, r, api.Err(http.StatusForbidden, "forbidden", "You can't edit this revision right now."))
		return
	}
	var in moveInput
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	from, to := in.clean()
	n, err := s.RewriteAfterRename(r.Context(), apply, repo, rev, c, from, to)
	if err != nil {
		fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"pages": n})
}
