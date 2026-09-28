package lifecycle

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/audit"
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/orghttp"
	"github.com/kmdn-app/kmdn/internal/orgs"
	"github.com/kmdn-app/kmdn/internal/store"
)

// JobPurge purges orgs past their grace period (hourly).
const JobPurge = "orgs.purge"

// Routes: an owner deletes the org, the instance console restores one,
// people erase their own account, instance admins erase anyone's.
func (s *Service) Routes(r chi.Router, h *auth.HTTP) {
	r.Group(func(r chi.Router) {
		r.Use(auth.Require)
		r.Delete("/me", func(w http.ResponseWriter, r *http.Request) { s.eraseSelf(w, r, h) })
	})
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin)
		r.Get("/admin/orgs/deleted", s.deleted)
		r.Post("/admin/orgs/{org}/restore", s.restore)
		r.Delete("/admin/users/{id}", s.eraseUser)
	})
}

// OrgRoutes go under /orgs/{org}.
func (s *Service) OrgRoutes(r chi.Router) {
	r.Delete("/", s.deleteOrg)
}

func actor(r *http.Request) audit.Entry {
	p, _ := auth.FromContext(r.Context())
	return audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID}
}

// deleteOrg: owners only, confirming with the org's address.
func (s *Service) deleteOrg(w http.ResponseWriter, r *http.Request) {
	c := orghttp.Current(r)
	cur, _ := orgs.FromContext(r.Context())
	p, _ := auth.FromContext(r.Context())
	if cur.Role != orgs.Owner && !p.User.IsInstanceAdmin {
		api.Error(w, r, api.Err(http.StatusForbidden, "org_owner_only", "Only owners can delete the organization."))
		return
	}
	var in struct {
		Confirm string `json:"confirm"`
	}
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	if in.Confirm != c.Slug {
		api.Error(w, r, api.Invalid("confirm", "Type the organization's address ("+c.Slug+") to confirm."))
		return
	}
	err := s.RequestDeletion(r.Context(), c.ID, actor(r))
	if errors.Is(err, ErrDefaultOrg) {
		api.Error(w, r, api.Err(http.StatusConflict, "orgs_single_mode", "This instance has a single organization; it can't be deleted."))
		return
	}
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"ok": true, "purge_after": time.Now().Add(s.grace()).UTC()})
}

// deleted lists orgs being deleted, for the instance console.
func (s *Service) deleted(w http.ResponseWriter, r *http.Request) {
	rows, err := store.Query(r.Context(), s.DB, `SELECT id, slug, name, deleted_at FROM orgs WHERE status = ? ORDER BY deleted_at`, orgs.Deleting)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	defer rows.Close()
	type item struct {
		ID         string    `json:"id"`
		Slug       string    `json:"slug"`
		Name       string    `json:"name"`
		DeletedAt  time.Time `json:"deleted_at"`
		PurgeAfter time.Time `json:"purge_after"`
	}
	out := []item{}
	for rows.Next() {
		var it item
		var at int64
		if err := rows.Scan(&it.ID, &it.Slug, &it.Name, &at); err != nil {
			api.Error(w, r, err)
			return
		}
		it.DeletedAt = store.FromMillis(at)
		it.PurgeAfter = it.DeletedAt.Add(s.grace())
		out = append(out, it)
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Service) restore(w http.ResponseWriter, r *http.Request) {
	o, err := orgs.BySlug(r.Context(), s.DB, chi.URLParam(r, "org"))
	if err != nil {
		api.Error(w, r, api.ErrNotFound)
		return
	}
	if err := s.Restore(r.Context(), o.ID, actor(r)); errors.Is(err, ErrNotDeleted) {
		api.Error(w, r, api.Err(http.StatusConflict, "not_deleted", "This organization isn't being deleted."))
		return
	} else if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Service) erase(w http.ResponseWriter, r *http.Request, userID string) bool {
	err := s.EraseUser(r.Context(), userID, actor(r))
	var sole *SoleOwnerError
	if errors.As(err, &sole) {
		api.Error(w, r, api.Err(http.StatusConflict, "sole_owner", "This account is the only owner of "+sole.Org+", which has other members. Make someone else an owner first, or delete the organization.").WithParam("org", sole.Org))
		return false
	}
	if errors.Is(err, store.ErrNotFound) {
		api.Error(w, r, api.ErrNotFound)
		return false
	}
	if err != nil {
		api.Error(w, r, err)
		return false
	}
	return true
}

// eraseSelf: people erase their own account, confirming with its email.
func (s *Service) eraseSelf(w http.ResponseWriter, r *http.Request, h *auth.HTTP) {
	p, _ := auth.FromContext(r.Context())
	var in struct {
		Confirm string `json:"confirm"`
	}
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	if !strings.EqualFold(strings.TrimSpace(in.Confirm), p.User.Email) {
		api.Error(w, r, api.Invalid("confirm", "Type your email address to confirm."))
		return
	}
	if s.erase(w, r, p.User.ID) {
		h.ClearCookies(w)
		api.JSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func (s *Service) eraseUser(w http.ResponseWriter, r *http.Request) {
	if s.erase(w, r, chi.URLParam(r, "id")) {
		api.JSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}
