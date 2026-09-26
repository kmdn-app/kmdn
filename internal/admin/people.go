package admin

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/audit"
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/groups"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

// People handles /admin/users, /admin/groups, /admin/audit and the user
// directory used by member pickers.
type People struct {
	DB   *store.DB
	Auth *auth.Service
}

func (h *People) Routes(r chi.Router) {
	r.With(auth.Require).Get("/users", h.directory)
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin)
		r.Get("/admin/users", h.listUsers)
		r.Patch("/admin/users/{id}", h.updateUser)
		r.Post("/admin/users/{id}/revoke-sessions", h.revokeSessions)
		r.Get("/admin/groups", h.listGroups)
		r.Post("/admin/groups", h.createGroup)
		r.Patch("/admin/groups/{id}", h.updateGroup)
		r.Delete("/admin/groups/{id}", h.deleteGroup)
		r.Get("/admin/groups/{id}/members", h.groupMembers)
		r.Put("/admin/groups/{id}/members/{user}", h.addGroupMember)
		r.Delete("/admin/groups/{id}/members/{user}", h.removeGroupMember)
		r.Get("/admin/audit", h.auditLog)
	})
}

// AdminUser is a user row in the admin console.
type AdminUser struct {
	users.User
	Repos    int `json:"repos"`
	Sessions int `json:"sessions"`
}

func (h *People) queryUsers(r *http.Request, where string, args ...any) ([]AdminUser, error) {
	rows, err := store.Query(r.Context(), h.DB, `SELECT u.id, u.email, u.name, u.locale, u.theme, u.commit_email_mode, u.is_instance_admin, u.status, u.created_at, u.last_active_at,
			(SELECT COUNT(DISTINCT m.repo_id) FROM repo_members m WHERE (m.principal_type = 'user' AND m.principal_id = u.id)
				OR (m.principal_type = 'group' AND m.principal_id IN (SELECT group_id FROM group_members g WHERE g.user_id = u.id))),
			(SELECT COUNT(*) FROM sessions s WHERE s.user_id = u.id AND s.expires_at > ?)
		FROM users u `+where+` ORDER BY u.name LIMIT 500`, append([]any{store.Millis(time.Now())}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminUser{}
	for rows.Next() {
		var u AdminUser
		var created int64
		var last sql.NullInt64
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.Locale, &u.Theme, &u.CommitEmailMode, &u.IsInstanceAdmin, &u.Status, &created, &last, &u.Repos, &u.Sessions); err != nil {
			return nil, err
		}
		u.CreatedAt, u.LastActiveAt = store.FromMillis(created), store.NullMillis(last)
		out = append(out, u)
	}
	return out, rows.Err()
}

func searchWhere(q string) (string, []any) {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return "", nil
	}
	like := "%" + strings.NewReplacer("%", "", "_", "").Replace(q) + "%"
	return `WHERE LOWER(u.name) LIKE ? OR u.email LIKE ?`, []any{like, like}
}

func (h *People) listUsers(w http.ResponseWriter, r *http.Request) {
	where, args := searchWhere(r.URL.Query().Get("q"))
	if st := r.URL.Query().Get("status"); st == users.Active || st == users.Deactivated {
		if where == "" {
			where = "WHERE u.status = ?"
		} else {
			where = "WHERE (" + strings.TrimPrefix(where, "WHERE ") + ") AND u.status = ?"
		}
		args = append(args, st)
	}
	list, err := h.queryUsers(r, where, args...)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": list})
}

// directory lets signed-in people find users by name or email (member pickers, @mentions).
func (h *People) directory(w http.ResponseWriter, r *http.Request) {
	where, args := searchWhere(r.URL.Query().Get("q"))
	if where == "" {
		where = "WHERE u.status = 'active'"
	} else {
		where = "WHERE (" + strings.TrimPrefix(where, "WHERE ") + ") AND u.status = 'active'"
	}
	list, err := h.queryUsers(r, where, args...)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	type entry struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	out := make([]entry, 0, len(list))
	for i, u := range list {
		if i >= 20 {
			break
		}
		out = append(out, entry{u.ID, u.Name, u.Email})
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *People) updateUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, _ := auth.FromContext(ctx)
	target, err := users.ByID(ctx, h.DB, chi.URLParam(r, "id"))
	if err != nil {
		api.Error(w, r, api.ErrNotFound)
		return
	}
	var body struct {
		IsInstanceAdmin *bool   `json:"is_instance_admin"`
		Status          *string `json:"status"`
		Name            *string `json:"name"`
	}
	if err := api.Decode(r, &body); err != nil {
		api.Error(w, r, err)
		return
	}
	err = h.DB.InTx(ctx, func(tx *store.Tx) error {
		demoting := body.IsInstanceAdmin != nil && !*body.IsInstanceAdmin && target.IsInstanceAdmin
		deactivating := body.Status != nil && *body.Status == users.Deactivated && target.Status == users.Active
		if (demoting || deactivating) && target.IsInstanceAdmin {
			n, err := users.CountAdmins(ctx, tx)
			if err != nil {
				return err
			}
			if n <= 1 {
				return api.Err(http.StatusConflict, "last_admin", "This is the only instance admin. Make someone else an admin first.")
			}
		}
		if deactivating && target.ID == p.User.ID {
			return api.Err(http.StatusConflict, "self", "You can't deactivate your own account.")
		}
		if body.IsInstanceAdmin != nil {
			if _, err := store.Exec(ctx, tx, `UPDATE users SET is_instance_admin = ? WHERE id = ?`, *body.IsInstanceAdmin, target.ID); err != nil {
				return err
			}
		}
		if body.Status != nil {
			if *body.Status != users.Active && *body.Status != users.Deactivated {
				return api.Invalid("status", "Status must be active or deactivated.")
			}
			if _, err := store.Exec(ctx, tx, `UPDATE users SET status = ? WHERE id = ?`, *body.Status, target.ID); err != nil {
				return err
			}
			if *body.Status == users.Deactivated {
				if _, err := store.Exec(ctx, tx, `DELETE FROM sessions WHERE user_id = ?`, target.ID); err != nil {
					return err
				}
			}
		}
		if body.Name != nil && strings.TrimSpace(*body.Name) != "" {
			if _, err := store.Exec(ctx, tx, `UPDATE users SET name = ? WHERE id = ?`, strings.TrimSpace(*body.Name), target.ID); err != nil {
				return err
			}
		}
		return audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID, Action: "user.updated", TargetType: "user", TargetID: target.ID,
			Data: map[string]any{"admin": body.IsInstanceAdmin, "status": body.Status}})
	})
	if err != nil {
		api.Error(w, r, err)
		return
	}
	list, err := h.queryUsers(r, "WHERE u.id = ?", target.ID)
	if err != nil || len(list) == 0 {
		api.Error(w, r, errors.Join(err, errors.New("user vanished")))
		return
	}
	api.JSON(w, http.StatusOK, list[0])
}

func (h *People) revokeSessions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, _ := auth.FromContext(ctx)
	id := chi.URLParam(r, "id")
	keep := ""
	if id == p.User.ID {
		keep = p.Session.ID
	}
	if err := h.Auth.RevokeUser(ctx, id, keep); err != nil {
		api.Error(w, r, err)
		return
	}
	_ = audit.Write(ctx, h.DB, audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID, Action: "user.sessions_revoked", TargetType: "user", TargetID: id})
	w.WriteHeader(http.StatusNoContent)
}

func (h *People) listGroups(w http.ResponseWriter, r *http.Request) {
	list, err := groups.List(r.Context(), h.DB)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": list})
}

type groupBody struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (b *groupBody) validate() error {
	b.Name, b.Description = strings.TrimSpace(b.Name), strings.TrimSpace(b.Description)
	if b.Name == "" || len(b.Name) > 80 {
		return api.Invalid("name", "Enter a group name (up to 80 characters).")
	}
	return nil
}

func (h *People) createGroup(w http.ResponseWriter, r *http.Request) {
	var b groupBody
	if err := api.Decode(r, &b); err != nil {
		api.Error(w, r, err)
		return
	}
	if err := b.validate(); err != nil {
		api.Error(w, r, err)
		return
	}
	g, err := groups.Create(r.Context(), h.DB, b.Name, b.Description)
	if store.IsUniqueViolation(err) {
		api.Error(w, r, api.Invalid("name", "A group with this name already exists."))
		return
	}
	if err != nil {
		api.Error(w, r, err)
		return
	}
	p, _ := auth.FromContext(r.Context())
	_ = audit.Write(r.Context(), h.DB, audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID, Action: "group.created", TargetType: "group", TargetID: g.ID})
	api.JSON(w, http.StatusCreated, g)
}

func (h *People) updateGroup(w http.ResponseWriter, r *http.Request) {
	var b groupBody
	if err := api.Decode(r, &b); err != nil {
		api.Error(w, r, err)
		return
	}
	if err := b.validate(); err != nil {
		api.Error(w, r, err)
		return
	}
	id := chi.URLParam(r, "id")
	if _, err := groups.Get(r.Context(), h.DB, id); err != nil {
		api.Error(w, r, api.ErrNotFound)
		return
	}
	if err := groups.Update(r.Context(), h.DB, id, b.Name, b.Description); err != nil {
		if store.IsUniqueViolation(err) {
			api.Error(w, r, api.Invalid("name", "A group with this name already exists."))
			return
		}
		api.Error(w, r, err)
		return
	}
	g, _ := groups.Get(r.Context(), h.DB, id)
	api.JSON(w, http.StatusOK, g)
}

func (h *People) deleteGroup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := groups.Delete(r.Context(), h.DB, id); err != nil {
		api.Error(w, r, err)
		return
	}
	p, _ := auth.FromContext(r.Context())
	_ = audit.Write(r.Context(), h.DB, audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID, Action: "group.deleted", TargetType: "group", TargetID: id})
	w.WriteHeader(http.StatusNoContent)
}

func (h *People) groupMembers(w http.ResponseWriter, r *http.Request) {
	ids, err := groups.MemberIDs(r.Context(), h.DB, chi.URLParam(r, "id"))
	if err != nil {
		api.Error(w, r, err)
		return
	}
	out := []AdminUser{}
	for _, id := range ids {
		if list, err := h.queryUsers(r, "WHERE u.id = ?", id); err == nil && len(list) == 1 {
			out = append(out, list[0])
		}
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *People) addGroupMember(w http.ResponseWriter, r *http.Request) {
	gid, uid := chi.URLParam(r, "id"), chi.URLParam(r, "user")
	if _, err := groups.Get(r.Context(), h.DB, gid); err != nil {
		api.Error(w, r, api.ErrNotFound)
		return
	}
	if _, err := users.ByID(r.Context(), h.DB, uid); err != nil {
		api.Error(w, r, api.Invalid("user", "No such user."))
		return
	}
	if err := groups.AddMember(r.Context(), h.DB, gid, uid); err != nil {
		api.Error(w, r, err)
		return
	}
	h.groupMembers(w, r)
}

func (h *People) removeGroupMember(w http.ResponseWriter, r *http.Request) {
	if err := groups.RemoveMember(r.Context(), h.DB, chi.URLParam(r, "id"), chi.URLParam(r, "user")); err != nil {
		api.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *People) auditLog(w http.ResponseWriter, r *http.Request) {
	list, err := audit.Recent(r.Context(), h.DB, 200)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	if list == nil {
		list = []audit.Record{}
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": list})
}
