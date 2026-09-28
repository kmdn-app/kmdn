// Package orghttp serves organizations over HTTP and resolves the org of
// org-scoped routes (/api/v1/orgs/{org}/…, docs/specs/16-organizations.md).
package orghttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/audit"
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/events"
	"github.com/kmdn-app/kmdn/internal/orgs"
	"github.com/kmdn-app/kmdn/internal/policy"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

// Service handles /orgs.
type Service struct {
	DB *store.DB
	// Settings reads and writes org settings, with the deployment's
	// managed fields.
	Settings *orgs.SettingsStore
	// AllowCreate is config orgs.allow_create: "admins" or "anyone".
	AllowCreate string
	// OrgForges says whether org admins can add their own forges.
	OrgForges bool
	// SignupURL is orgs.signup_url, for people without an org.
	SignupURL string
	Events    events.Sink
	Log       *slog.Logger
}

// View is an org as the API returns it, with the caller's role.
type View struct {
	orgs.Org
	Role string `json:"role"`
	// SignIn is set when the session can't act in the org until the person
	// signs in the way it asks.
	SignIn *policy.SignInRequired `json:"sign_in,omitempty"`
}

func signInRequired(ctx context.Context, orgID string) *policy.SignInRequired {
	var req *policy.SignInRequired
	if errors.As(orgs.SignInBlocked(ctx, orgID), &req) {
		return req
	}
	return nil
}

var (
	errSingle   = api.Err(http.StatusConflict, "orgs_single_mode", "This instance has a single organization. Set orgs.mode to multi to create more.")
	errNoCreate = api.Err(http.StatusForbidden, "orgs_create_forbidden", "Only instance admins can create organizations here.")
	errSlug     = api.Err(http.StatusConflict, "org_slug_taken", "Another organization already uses this address. Choose another.")
	errOwner    = api.Err(http.StatusConflict, "org_last_owner", "An organization needs at least one owner. Make someone else an owner first.")
)

// Routes registers /orgs and /orgs/{org}, and mounts each scoped
// function's routes under /orgs/{org} behind Resolve.
func (s *Service) Routes(r chi.Router, scoped ...func(chi.Router)) {
	r.Group(func(r chi.Router) {
		r.Use(auth.Require)
		r.Get("/orgs", s.list)
		r.Post("/orgs", s.create)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAdmin)
			r.Get("/admin/orgs", s.instanceList)
			r.Patch("/admin/orgs/{org}", s.instanceUpdate)
			r.Post("/admin/orgs/{org}/domains/{domain}/verify", s.verifyDomain)
		})
		r.Route("/orgs/{org}", func(r chi.Router) {
			r.Use(s.Resolve)
			r.Get("/", s.get)
			r.With(RequireAdmin).Patch("/", s.update)
			r.With(RequireAdmin).Get("/members", s.members)
			r.With(RequireAdmin).Patch("/members/{user}", s.updateMember)
			r.Delete("/members/{user}", s.removeMember)
			r.With(RequireAdmin).Get("/admin/settings", s.getSettings)
			r.With(RequireAdmin).Patch("/admin/settings", s.updateSettings)
			r.With(RequireAdmin).Get("/admin/domains", s.listDomains)
			r.With(RequireAdmin).Post("/admin/domains", s.addDomain)
			r.With(RequireAdmin).Delete("/admin/domains/{domain}", s.removeDomain)
			for _, fn := range scoped {
				fn(r)
			}
		})
	})
}

// Resolve loads the {org} of the path and the caller's role in it. People
// outside the org get 404, so slugs can't be probed; instance admins reach
// any org.
func (s *Service) Resolve(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := auth.FromContext(r.Context())
		o, err := orgs.BySlug(r.Context(), s.DB, chi.URLParam(r, "org"))
		if errors.Is(err, store.ErrNotFound) || (err == nil && o.Status == orgs.Deleting) {
			api.Error(w, r, api.ErrNotFound)
			return
		}
		if err != nil {
			api.Error(w, r, err)
			return
		}
		role, err := orgs.Role(r.Context(), s.DB, o.ID, p.User)
		if err != nil {
			api.Error(w, r, err)
			return
		}
		if role == "" && !p.User.IsInstanceAdmin {
			// A member whose session the org doesn't accept learns how to
			// sign in; everyone else gets 404.
			if req := signInRequired(r.Context(), o.ID); req != nil {
				if member, _ := orgs.MemberRole(r.Context(), s.DB, o.ID, p.User); member != "" {
					api.Error(w, r, req.Problem())
					return
				}
			}
			api.Error(w, r, api.ErrNotFound)
			return
		}
		// On Postgres, row-level security keeps this request's statements to
		// the org's rows.
		ctx := store.WithOrg(orgs.WithCurrent(r.Context(), orgs.Current{Org: o, Role: role}), o.ID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireAdmin rejects callers who don't administer the resolved org.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, ok := orgs.FromContext(r.Context())
		p, _ := auth.FromContext(r.Context())
		if !ok || !c.Admin(p.User.IsInstanceAdmin) {
			api.Error(w, r, api.ErrForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Current returns the resolved org; handlers mounted under /orgs/{org}
// always have one.
func Current(r *http.Request) orgs.Org {
	c, _ := orgs.FromContext(r.Context())
	return c.Org
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	list, err := orgs.ForUser(r.Context(), s.DB, p.User)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	out := make([]View, 0, len(list))
	for _, o := range list {
		role, err := orgs.Role(r.Context(), s.DB, o.ID, p.User)
		if err != nil {
			api.Error(w, r, err)
			return
		}
		v := View{Org: o, Role: role}
		if role == "" && !p.User.IsInstanceAdmin {
			if v.SignIn = signInRequired(r.Context(), o.ID); v.SignIn == nil {
				out = append(out, v)
				continue
			}
			if v.Role, err = orgs.MemberRole(r.Context(), s.DB, o.ID, p.User); err != nil {
				api.Error(w, r, err)
				return
			}
		}
		out = append(out, v)
	}
	mode, err := orgs.Mode(r.Context(), s.DB)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	canCreate := mode == orgs.Multi && (s.AllowCreate == "anyone" || p.User.IsInstanceAdmin)
	api.JSON(w, http.StatusOK, map[string]any{"items": out, "mode": mode, "can_create": canCreate, "org_forges": s.OrgForges, "signup_url": s.SignupURL})
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, _ := auth.FromContext(ctx)
	var in struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	mode, err := orgs.Mode(ctx, s.DB)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	switch {
	case mode == orgs.Single:
		api.Error(w, r, errSingle)
		return
	case s.AllowCreate != "anyone" && !p.User.IsInstanceAdmin:
		api.Error(w, r, errNoCreate)
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 100 {
		api.Error(w, r, api.Invalid("name", "Give the organization a name (up to 100 characters)."))
		return
	}
	var o orgs.Org
	err = s.DB.InTx(ctx, func(tx *store.Tx) error {
		if in.Slug != "" {
			o, err = orgs.Create(ctx, tx, in.Slug, in.Name, p.User.ID)
		} else {
			o, err = createFree(ctx, tx, in.Name, p.User.ID)
		}
		if err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID, OrgID: o.ID, Action: "org.created", TargetType: "org", TargetID: o.ID,
			Data: map[string]any{"slug": o.Slug}})
	})
	if !s.slugError(w, r, err) {
		return
	}
	s.Events.Emit(ctx, events.Event{Type: events.OrgCreated, OrgID: o.ID, UserID: p.User.ID, Data: map[string]any{"slug": o.Slug}})
	s.Events.Emit(ctx, events.Event{Type: events.MemberAdded, OrgID: o.ID, UserID: p.User.ID, Data: map[string]any{"role": orgs.Owner}})
	api.JSON(w, http.StatusCreated, View{Org: o, Role: orgs.Owner})
}

// createFree creates an org with a slug made from its name, adding -2, -3…
// until one is free.
func createFree(ctx context.Context, q store.Querier, name, owner string) (orgs.Org, error) {
	base := orgs.Slugify(name)
	for i := 1; i <= 100; i++ {
		slug := base
		if i > 1 {
			suffix := fmt.Sprintf("-%d", i)
			slug = strings.TrimSuffix(base[:min(len(base), 39-len(suffix))], "-") + suffix
		}
		o, err := orgs.Create(ctx, q, slug, name, owner)
		if !errors.Is(err, orgs.ErrSlugTaken) {
			return o, err
		}
	}
	return orgs.Org{}, orgs.ErrSlugTaken
}

// slugError writes slug problems and other errors; it reports whether err
// was nil.
func (s *Service) slugError(w http.ResponseWriter, r *http.Request, err error) bool {
	var bad *orgs.ErrSlug
	switch {
	case err == nil:
		return true
	case errors.As(err, &bad):
		api.Error(w, r, api.Invalid("slug", bad.Reason))
	case errors.Is(err, orgs.ErrSlugTaken):
		api.Error(w, r, errSlug)
	default:
		api.Error(w, r, err)
	}
	return false
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	c, _ := orgs.FromContext(r.Context())
	api.JSON(w, http.StatusOK, View{Org: c.Org, Role: c.Role})
}

func (s *Service) update(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, _ := auth.FromContext(ctx)
	c, _ := orgs.FromContext(ctx)
	var in struct {
		Name *string `json:"name"`
		Slug *string `json:"slug"`
	}
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	if in.Slug != nil && *in.Slug != c.Org.Slug && c.Role != orgs.Owner && !p.User.IsInstanceAdmin {
		api.Error(w, r, api.Err(http.StatusForbidden, "org_owner_only", "Only owners can change the organization's address."))
		return
	}
	if in.Name != nil && (strings.TrimSpace(*in.Name) == "" || len(*in.Name) > 100) {
		api.Error(w, r, api.Invalid("name", "Give the organization a name (up to 100 characters)."))
		return
	}
	err := s.DB.InTx(ctx, func(tx *store.Tx) error {
		if in.Name != nil {
			if err := orgs.Rename(ctx, tx, c.Org.ID, *in.Name); err != nil {
				return err
			}
		}
		if in.Slug != nil && *in.Slug != c.Org.Slug {
			if err := orgs.SetSlug(ctx, tx, c.Org.ID, *in.Slug); err != nil {
				return err
			}
		}
		return audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID, OrgID: c.Org.ID, Action: "org.updated", TargetType: "org", TargetID: c.Org.ID,
			Data: map[string]any{"name": in.Name, "slug": in.Slug}})
	})
	if !s.slugError(w, r, err) {
		return
	}
	o, err := orgs.ByID(ctx, s.DB, c.Org.ID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, View{Org: o, Role: c.Role})
}

func (s *Service) getSettings(w http.ResponseWriter, r *http.Request) {
	st, locked, err := s.Settings.Get(r.Context(), Current(r).ID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"settings": st, "locked": locked})
}

func (s *Service) updateSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, _ := auth.FromContext(ctx)
	var patch map[string]json.RawMessage
	if err := api.Decode(r, &patch); err != nil {
		api.Error(w, r, err)
		return
	}
	org := Current(r)
	var st orgs.Settings
	err := s.DB.InTx(ctx, func(tx *store.Tx) error {
		var err error
		if st, err = s.Settings.Update(ctx, tx, org.ID, patch); err != nil {
			return err
		}
		fields := make([]string, 0, len(patch))
		for k := range patch {
			fields = append(fields, k)
		}
		return audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID, OrgID: org.ID, Action: "org.settings_changed", TargetType: "org", TargetID: org.ID,
			Data: map[string]any{"fields": fields}})
	})
	var syntax *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	var bad *orgs.ErrInvalid
	switch {
	case errors.As(err, &bad):
		api.Error(w, r, api.Invalid(bad.Field, bad.Reason))
		return
	case errors.Is(err, orgs.ErrLocked):
		api.Error(w, r, api.Err(http.StatusConflict, "setting_managed", "This setting is managed by the deployment and can't be changed here."))
		return
	case errors.As(err, &syntax), errors.As(err, &typeErr), err != nil && strings.HasPrefix(err.Error(), "json: unknown field"):
		api.Error(w, r, api.Invalid("settings", "Unknown setting or wrong type: "+err.Error()))
		return
	case err != nil:
		api.Error(w, r, err)
		return
	}
	_, locked, _ := s.Settings.Get(ctx, org.ID)
	api.JSON(w, http.StatusOK, map[string]any{"settings": st, "locked": locked})
}

func (s *Service) listDomains(w http.ResponseWriter, r *http.Request) {
	list, err := orgs.Domains(r.Context(), s.DB, Current(r).ID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": list})
}

// addDomain claims an email domain for the org. People with addresses there
// join the org on their own once the deployment verifies it.
func (s *Service) addDomain(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, _ := auth.FromContext(ctx)
	var in struct {
		Domain string `json:"domain"`
	}
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	org := Current(r)
	var d orgs.Domain
	err := s.DB.InTx(ctx, func(tx *store.Tx) error {
		var err error
		if d, err = orgs.AddDomain(ctx, tx, org.ID, in.Domain); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID, OrgID: org.ID, Action: "org.domain_added", TargetType: "org", TargetID: org.ID, Data: map[string]any{"domain": d.Domain}})
	})
	switch {
	case errors.Is(err, orgs.ErrDomain):
		api.Error(w, r, api.Invalid("domain", "Enter an email domain, e.g. northwind.dev."))
	case errors.Is(err, orgs.ErrDomainTaken):
		api.Error(w, r, api.Err(http.StatusConflict, "domain_taken", "Another organization has verified this domain."))
	case err != nil:
		api.Error(w, r, err)
	default:
		api.JSON(w, http.StatusCreated, d)
	}
}

func (s *Service) removeDomain(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, _ := auth.FromContext(ctx)
	org, domain := Current(r), chi.URLParam(r, "domain")
	if err := orgs.RemoveDomain(ctx, s.DB, org.ID, domain); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			api.Error(w, r, api.ErrNotFound)
			return
		}
		api.Error(w, r, err)
		return
	}
	_ = audit.Write(ctx, s.DB, audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID, OrgID: org.ID, Action: "org.domain_removed", TargetType: "org", TargetID: org.ID, Data: map[string]any{"domain": domain}})
	w.WriteHeader(http.StatusNoContent)
}

// instanceList lists every org (instance console).
func (s *Service) instanceList(w http.ResponseWriter, r *http.Request) {
	list, err := orgs.List(r.Context(), s.DB)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": list})
}

// instanceUpdate suspends or resumes an org (instance console).
func (s *Service) instanceUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, _ := auth.FromContext(ctx)
	o, err := orgs.BySlug(ctx, s.DB, chi.URLParam(r, "org"))
	if err != nil {
		api.Error(w, r, api.ErrNotFound)
		return
	}
	var in struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	if in.Status != orgs.Active && in.Status != orgs.Suspended {
		api.Error(w, r, api.Invalid("status", "Use active or suspended."))
		return
	}
	err = s.DB.InTx(ctx, func(tx *store.Tx) error {
		if err := orgs.SetStatus(ctx, tx, o.ID, in.Status, strings.TrimSpace(in.Reason)); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID, OrgID: o.ID, Action: "org.status_changed", TargetType: "org", TargetID: o.ID, Data: map[string]any{"status": in.Status}})
	})
	if err != nil {
		api.Error(w, r, err)
		return
	}
	o, _ = orgs.ByID(ctx, s.DB, o.ID)
	s.Events.Emit(ctx, events.Event{Type: events.OrgStatusChanged, OrgID: o.ID, UserID: p.User.ID, Data: map[string]any{"status": o.Status, "reason": o.StatusReason}})
	api.JSON(w, http.StatusOK, o)
}

// verifyDomain marks an org's domain verified (instance admins; a hosted
// service verifies by DNS instead).
func (s *Service) verifyDomain(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, _ := auth.FromContext(ctx)
	o, err := orgs.BySlug(ctx, s.DB, chi.URLParam(r, "org"))
	if err != nil {
		api.Error(w, r, api.ErrNotFound)
		return
	}
	domain := chi.URLParam(r, "domain")
	if err := orgs.VerifyDomain(ctx, s.DB, o.ID, domain); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			api.Error(w, r, api.ErrNotFound)
			return
		}
		api.Error(w, r, err)
		return
	}
	_ = audit.Write(ctx, s.DB, audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID, OrgID: o.ID, Action: "org.domain_verified", TargetType: "org", TargetID: o.ID, Data: map[string]any{"domain": domain}})
	w.WriteHeader(http.StatusNoContent)
}

// MemberView is a member in the org console.
type MemberView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Role     string `json:"role"`
	Status   string `json:"status"`
	JoinedAt int64  `json:"joined_at"`
}

// members lists the org's members. In single mode that is every account,
// with the roles the membership rows give (members otherwise).
func (s *Service) members(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, _ := orgs.FromContext(ctx)
	where, args, err := orgs.MemberClause(ctx, s.DB, c.Org.ID, "u.id")
	if err != nil {
		api.Error(w, r, err)
		return
	}
	rows, err := store.Query(ctx, s.DB, `SELECT u.id, u.name, u.email, u.is_instance_admin, u.status, u.created_at, COALESCE(m.role, ''), COALESCE(m.status, ''), COALESCE(m.joined_at, 0)
		FROM users u LEFT JOIN org_members m ON m.org_id = ? AND m.user_id = u.id
		WHERE `+where+` ORDER BY u.name LIMIT 1000`, append([]any{c.Org.ID}, args...)...)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	defer rows.Close()
	mode, err := orgs.Mode(ctx, s.DB)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	single := mode == orgs.Single
	out := []MemberView{}
	for rows.Next() {
		var v MemberView
		var admin bool
		var created int64
		var status string
		if err := rows.Scan(&v.ID, &v.Name, &v.Email, &admin, &v.Status, &created, &v.Role, &status, &v.JoinedAt); err != nil {
			api.Error(w, r, err)
			return
		}
		if status == orgs.Deactivated {
			v.Status = orgs.Deactivated
		}
		if v.Role == "" {
			v.Role, v.JoinedAt = orgs.Member, created
		}
		if single && admin {
			v.Role = orgs.Owner
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Service) updateMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, _ := auth.FromContext(ctx)
	c, _ := orgs.FromContext(ctx)
	var in struct {
		Role   *string `json:"role"`
		Status *string `json:"status"`
	}
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	target, current, ok := s.member(w, r, c)
	if !ok {
		return
	}
	if in.Role != nil && !orgs.ValidRole(*in.Role) {
		api.Error(w, r, api.Invalid("role", "Use member, admin or owner."))
		return
	}
	if in.Status != nil && *in.Status != orgs.Active && *in.Status != orgs.Deactivated {
		api.Error(w, r, api.Invalid("status", "Use active or deactivated."))
		return
	}
	isOwner := c.Role == orgs.Owner || p.User.IsInstanceAdmin
	if (in.Role != nil && (*in.Role == orgs.Owner || current == orgs.Owner)) && !isOwner {
		api.Error(w, r, api.Err(http.StatusForbidden, "org_owner_only", "Only owners can add or remove owners."))
		return
	}
	losesOwner := current == orgs.Owner && ((in.Role != nil && *in.Role != orgs.Owner) || (in.Status != nil && *in.Status == orgs.Deactivated))
	err := s.DB.InTx(ctx, func(tx *store.Tx) error {
		if losesOwner {
			if err := lastOwner(ctx, tx, c.Org.ID); err != nil {
				return err
			}
		}
		role := current
		if in.Role != nil {
			role = *in.Role
		}
		if err := orgs.AddMember(ctx, tx, c.Org.ID, target.ID, role, p.User.ID); err != nil {
			return err
		}
		if in.Status != nil && *in.Status == orgs.Deactivated {
			if err := orgs.SetMemberStatus(ctx, tx, c.Org.ID, target.ID, orgs.Deactivated); err != nil {
				return err
			}
		}
		return audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID, OrgID: c.Org.ID, Action: "org.member_changed", TargetType: "user", TargetID: target.ID,
			Data: map[string]any{"role": in.Role, "status": in.Status}})
	})
	if errors.Is(err, errOwner) {
		api.Error(w, r, errOwner)
		return
	}
	if err != nil {
		api.Error(w, r, err)
		return
	}
	s.Events.Emit(ctx, events.Event{Type: events.MemberChanged, OrgID: c.Org.ID, UserID: target.ID, Data: map[string]any{"role": in.Role, "status": in.Status}})
	api.JSON(w, http.StatusOK, map[string]any{"ok": true})
}

// removeMember takes someone out of the org: org admins remove others,
// anyone may leave.
func (s *Service) removeMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, _ := auth.FromContext(ctx)
	c, _ := orgs.FromContext(ctx)
	target, current, ok := s.member(w, r, c)
	if !ok {
		return
	}
	if target.ID != p.User.ID && !c.Admin(p.User.IsInstanceAdmin) {
		api.Error(w, r, api.ErrForbidden)
		return
	}
	if current == orgs.Owner && c.Role != orgs.Owner && !p.User.IsInstanceAdmin {
		api.Error(w, r, api.Err(http.StatusForbidden, "org_owner_only", "Only owners can remove an owner."))
		return
	}
	if c.Org.ID == orgs.DefaultID {
		if mode, err := orgs.Mode(ctx, s.DB); err != nil || mode == orgs.Single {
			api.Error(w, r, api.Err(http.StatusConflict, "orgs_single_mode", "Everyone belongs to this organization; deactivate the account instead."))
			return
		}
	}
	err := s.DB.InTx(ctx, func(tx *store.Tx) error {
		if current == orgs.Owner {
			if err := lastOwner(ctx, tx, c.Org.ID); err != nil {
				return err
			}
		}
		if err := orgs.RemoveMember(ctx, tx, c.Org.ID, target.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID, OrgID: c.Org.ID, Action: "org.member_removed", TargetType: "user", TargetID: target.ID})
	})
	if errors.Is(err, errOwner) {
		api.Error(w, r, errOwner)
		return
	}
	if err != nil {
		api.Error(w, r, err)
		return
	}
	s.Events.Emit(ctx, events.Event{Type: events.MemberRemoved, OrgID: c.Org.ID, UserID: target.ID})
	api.JSON(w, http.StatusOK, map[string]any{"ok": true})
}

// member loads the {user} of the path and their current role; people
// outside the org are 404.
func (s *Service) member(w http.ResponseWriter, r *http.Request, c orgs.Current) (users.User, string, bool) {
	u, err := users.ByID(r.Context(), s.DB, chi.URLParam(r, "user"))
	if err != nil {
		api.Error(w, r, api.ErrNotFound)
		return u, "", false
	}
	role, err := orgs.Role(r.Context(), s.DB, c.Org.ID, u)
	if err != nil {
		api.Error(w, r, err)
		return u, "", false
	}
	if role == "" {
		api.Error(w, r, api.ErrNotFound)
		return u, "", false
	}
	return u, role, true
}

// lastOwner fails when the org has a single owner left (called before that
// owner is demoted, deactivated or removed).
func lastOwner(ctx context.Context, q store.Querier, orgID string) error {
	n, err := orgs.CountOwners(ctx, q, orgID)
	if err != nil {
		return err
	}
	if n <= 1 {
		return errOwner
	}
	return nil
}
