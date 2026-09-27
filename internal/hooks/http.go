package hooks

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/store"
)

// Routes registers hook endpoints (repository admins).
func (s *Service) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.Require)
		r.Get("/repos/{repo}/hooks", s.list)
		r.Post("/repos/{repo}/hooks", s.create)
		r.Patch("/repos/{repo}/hooks/{hook}", s.update)
		r.Delete("/repos/{repo}/hooks/{hook}", s.delete)
		r.Post("/repos/{repo}/hooks/{hook}/ping", s.ping)
		r.Get("/repos/{repo}/hooks/{hook}/deliveries", s.deliveries)
		r.Post("/repos/{repo}/hooks/{hook}/deliveries/{delivery}/redeliver", s.redeliver)
	})
}

func (s *Service) repo(w http.ResponseWriter, r *http.Request) (repos.Repo, string, bool) {
	p, _ := auth.FromContext(r.Context())
	repo, err := repos.Get(r.Context(), s.DB, chi.URLParam(r, "repo"))
	if err != nil {
		api.Error(w, r, api.ErrNotFound)
		return repo, "", false
	}
	role, err := access.Effective(r.Context(), s.DB, p.User, repo.ID)
	if err != nil {
		api.Error(w, r, err)
		return repo, "", false
	}
	if role == access.None {
		api.Error(w, r, api.ErrNotFound)
		return repo, "", false
	}
	if !role.AtLeast(access.Admin) {
		api.Error(w, r, api.Err(http.StatusForbidden, "forbidden", "You need the admin role on this repository."))
		return repo, "", false
	}
	return repo, p.User.ID, true
}

func (s *Service) hook(w http.ResponseWriter, r *http.Request) (Hook, bool) {
	repo, _, ok := s.repo(w, r)
	if !ok {
		return Hook{}, false
	}
	h, err := GetHook(r.Context(), s.DB, repo.ID, chi.URLParam(r, "hook"))
	if err != nil {
		api.Error(w, r, api.ErrNotFound)
		return h, false
	}
	return h, true
}

func (s *Service) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ie *invalidErr
	if errors.As(err, &ie) {
		api.Error(w, r, api.Invalid(ie.field, ie.msg))
		return
	}
	api.Error(w, r, err)
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := s.repo(w, r)
	if !ok {
		return
	}
	hs, err := List(r.Context(), s.DB, repo.ID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	for i := range hs {
		if ds, err := Deliveries(r.Context(), s.DB, hs[i].ID, 1); err == nil && len(ds) > 0 {
			hs[i].LastDelivery = &ds[0]
		}
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": hs, "events": Events})
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	repo, uid, ok := s.repo(w, r)
	if !ok {
		return
	}
	var in Input
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	h, secret, err := s.Create(r.Context(), repo.ID, uid, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := map[string]any{"hook": h}
	if secret != "" {
		out["secret"] = secret // shown once
	}
	api.JSON(w, http.StatusCreated, out)
}

func (s *Service) update(w http.ResponseWriter, r *http.Request) {
	h, ok := s.hook(w, r)
	if !ok {
		return
	}
	var in Input
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	h, err := s.Update(r.Context(), h, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, h)
}

func (s *Service) delete(w http.ResponseWriter, r *http.Request) {
	h, ok := s.hook(w, r)
	if !ok {
		return
	}
	if err := s.Delete(r.Context(), h); err != nil {
		api.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) ping(w http.ResponseWriter, r *http.Request) {
	h, ok := s.hook(w, r)
	if !ok {
		return
	}
	if err := s.Ping(r.Context(), h); err != nil {
		api.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Service) deliveries(w http.ResponseWriter, r *http.Request) {
	h, ok := s.hook(w, r)
	if !ok {
		return
	}
	ds, err := Deliveries(r.Context(), s.DB, h.ID, 50)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": ds})
}

func (s *Service) redeliver(w http.ResponseWriter, r *http.Request) {
	h, ok := s.hook(w, r)
	if !ok {
		return
	}
	if err := s.Redeliver(r.Context(), h, chi.URLParam(r, "delivery")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			api.Error(w, r, api.ErrNotFound)
			return
		}
		api.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
