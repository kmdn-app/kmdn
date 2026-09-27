package admin

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/audit"
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/groups"
	"github.com/kmdn-app/kmdn/internal/orghttp"
	"github.com/kmdn-app/kmdn/internal/orgs"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

// People handles the instance's accounts (/admin/users), the audit log, and
// each org's directory (member pickers), groups and audit log.
type People struct {
	DB   *store.DB
	Auth *auth.Service
	Log  *slog.Logger
}

// Routes registers the instance console's people and audit endpoints.
func (h *People) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin)
		r.Get("/admin/users", h.listUsers)
		r.Patch("/admin/users/{id}", h.updateUser)
		r.Post("/admin/users/{id}/revoke-sessions", h.revokeSessions)
		r.Get("/admin/audit", h.auditLog)
		r.Get("/admin/audit/export", h.auditExport)
	})
}

// OrgRoutes registers an org's directory, groups and audit log (under
// /orgs/{org}).
func (h *People) OrgRoutes(r chi.Router) {
	r.Get("/users", h.directory)
	r.Get("/groups", h.listGroups)
	r.Group(func(r chi.Router) {
		r.Use(orghttp.RequireAdmin)
		r.Get("/admin/groups", h.listGroups)
		r.Post("/admin/groups", h.createGroup)
		r.Patch("/admin/groups/{id}", h.updateGroup)
		r.Delete("/admin/groups/{id}", h.deleteGroup)
		r.Get("/admin/groups/{id}/members", h.groupMembers)
		r.Put("/admin/groups/{id}/members/{user}", h.addGroupMember)
		r.Delete("/admin/groups/{id}/members/{user}", h.removeGroupMember)
		r.Get("/admin/audit", h.auditLog)
		r.Get("/admin/audit/export", h.auditExport)
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

// directory lets an org's members find each other by name or email (member
// pickers, @mentions).
func (h *People) directory(w http.ResponseWriter, r *http.Request) {
	members, margs, err := orgs.MemberClause(r.Context(), h.DB, orghttp.Current(r).ID, "u.id")
	if err != nil {
		api.Error(w, r, err)
		return
	}
	where, args := searchWhere(r.URL.Query().Get("q"))
	if where == "" {
		where = "WHERE u.status = 'active' AND " + members
	} else {
		where = "WHERE (" + strings.TrimPrefix(where, "WHERE ") + ") AND u.status = 'active' AND " + members
	}
	args = append(args, margs...)
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
	list, err := groups.List(r.Context(), h.DB, orghttp.Current(r).ID)
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
	g, err := groups.Create(r.Context(), h.DB, orghttp.Current(r).ID, b.Name, b.Description)
	if store.IsUniqueViolation(err) {
		api.Error(w, r, api.Invalid("name", "A group with this name already exists."))
		return
	}
	if err != nil {
		api.Error(w, r, err)
		return
	}
	p, _ := auth.FromContext(r.Context())
	_ = audit.Write(r.Context(), h.DB, audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID, OrgID: g.OrgID, Action: "group.created", TargetType: "group", TargetID: g.ID})
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
	if _, ok := h.group(w, r); !ok {
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
	h.audit(r, "group.updated", "group", id, map[string]any{"name": b.Name})
	g, _ := groups.Get(r.Context(), h.DB, id)
	api.JSON(w, http.StatusOK, g)
}

// audit records an admin's action (in the org of the request, if any).
func (h *People) audit(r *http.Request, action, targetType, targetID string, data map[string]any) {
	p, _ := auth.FromContext(r.Context())
	c, _ := orgs.FromContext(r.Context())
	_ = audit.Write(r.Context(), h.DB, audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID, OrgID: c.Org.ID, Action: action, TargetType: targetType, TargetID: targetID, Data: data})
}

// group loads the {id} group of the path; groups of other orgs are 404.
func (h *People) group(w http.ResponseWriter, r *http.Request) (groups.Group, bool) {
	g, err := groups.Get(r.Context(), h.DB, chi.URLParam(r, "id"))
	if err != nil || g.OrgID != orghttp.Current(r).ID {
		api.Error(w, r, api.ErrNotFound)
		return g, false
	}
	return g, true
}

func (h *People) deleteGroup(w http.ResponseWriter, r *http.Request) {
	g, ok := h.group(w, r)
	if !ok {
		return
	}
	if err := groups.Delete(r.Context(), h.DB, g.ID); err != nil {
		api.Error(w, r, err)
		return
	}
	h.audit(r, "group.deleted", "group", g.ID, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (h *People) groupMembers(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.group(w, r); !ok {
		return
	}
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
	g, ok := h.group(w, r)
	if !ok {
		return
	}
	// Only the org's members can join its groups.
	u, err := users.ByID(r.Context(), h.DB, uid)
	in := false
	if err == nil {
		in, err = orgs.Belongs(r.Context(), h.DB, g.OrgID, u)
	}
	if err != nil || !in {
		api.Error(w, r, api.Invalid("user", "No such person in this organization."))
		return
	}
	if err := groups.AddMember(r.Context(), h.DB, gid, uid); err != nil {
		api.Error(w, r, err)
		return
	}
	h.audit(r, "group.member_added", "group", gid, map[string]any{"user_id": uid})
	h.groupMembers(w, r)
}

func (h *People) removeGroupMember(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.group(w, r); !ok {
		return
	}
	if err := groups.RemoveMember(r.Context(), h.DB, chi.URLParam(r, "id"), chi.URLParam(r, "user")); err != nil {
		api.Error(w, r, err)
		return
	}
	h.audit(r, "group.member_removed", "group", chi.URLParam(r, "id"), map[string]any{"user_id": chi.URLParam(r, "user")})
	w.WriteHeader(http.StatusNoContent)
}

// auditFilter reads the audit log filters: actor_type, actor_id, action
// (exact, or a family ending in "."), repo, from and to (YYYY-MM-DD, UTC,
// both inclusive) and cursor.
func auditFilter(r *http.Request) (audit.Filter, error) {
	q := r.URL.Query()
	f := audit.Filter{ActorType: q.Get("actor_type"), ActorID: q.Get("actor_id"), Action: q.Get("action"), RepoID: q.Get("repo")}
	// Under /orgs/{org}, only that org's entries.
	if c, ok := orgs.FromContext(r.Context()); ok {
		f.OrgID = c.Org.ID
	}
	for _, d := range []struct {
		key string
		to  *time.Time
		add int
	}{{"from", &f.From, 0}, {"to", &f.To, 1}} {
		if v := q.Get(d.key); v != "" {
			t, err := time.Parse(time.DateOnly, v)
			if err != nil {
				return f, api.Invalid(d.key, "Use a date like 2026-09-27.")
			}
			*d.to = t.AddDate(0, 0, d.add)
		}
	}
	if c := q.Get("cursor"); c != "" {
		ms, id, ok := strings.Cut(c, "_")
		n, err := strconv.ParseInt(ms, 10, 64)
		if !ok || err != nil {
			return f, api.Invalid("cursor", "Invalid cursor.")
		}
		f.Before = &audit.Record{ID: id, At: store.FromMillis(n)}
	}
	return f, nil
}

func (h *People) auditLog(w http.ResponseWriter, r *http.Request) {
	f, err := auditFilter(r)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	f.Limit = 100
	list, err := audit.Query(r.Context(), h.DB, f)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	out := map[string]any{"items": list}
	if len(list) == f.Limit {
		last := list[len(list)-1]
		out["next_cursor"] = strconv.FormatInt(store.Millis(last.At), 10) + "_" + last.ID
	}
	if r.URL.Query().Get("cursor") == "" {
		actions, err := audit.Actions(r.Context(), h.DB, f.OrgID)
		if err != nil {
			api.Error(w, r, err)
			return
		}
		out["actions"] = actions
	}
	api.JSON(w, http.StatusOK, out)
}

// auditExport streams every matching entry as NDJSON or CSV.
func (h *People) auditExport(w http.ResponseWriter, r *http.Request) {
	f, err := auditFilter(r)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	format := r.URL.Query().Get("format")
	if format != "csv" && format != "ndjson" {
		api.Error(w, r, api.Invalid("format", "format is csv or ndjson."))
		return
	}
	name := "kmdn-audit-" + time.Now().UTC().Format("20060102-150405")
	var cw *csv.Writer
	enc := json.NewEncoder(w)
	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.csv"`)
		cw = csv.NewWriter(w)
		_ = cw.Write([]string{"id", "at", "actor_type", "actor_id", "actor_name", "ip", "action", "target_type", "target_id", "repo_id", "repo_name", "data"})
	} else {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.ndjson"`)
	}
	p, _ := auth.FromContext(r.Context())
	_ = audit.Write(r.Context(), h.DB, audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID, Action: "audit.exported", Data: map[string]any{"format": format, "filter": r.URL.RawQuery}})
	f.Limit = 1000
	for {
		list, err := audit.Query(r.Context(), h.DB, f)
		if err != nil {
			h.Log.Error("audit export", "err", err)
			return // headers are sent: the file ends early
		}
		for _, e := range list {
			if cw != nil {
				data, _ := json.Marshal(e.Data)
				if e.Data == nil {
					data = []byte("{}")
				}
				_ = cw.Write([]string{e.ID, e.At.UTC().Format(time.RFC3339Nano), e.ActorType, e.ActorID, e.ActorName, e.IP, e.Action, e.TargetType, e.TargetID, e.RepoID, e.RepoName, string(data)})
			} else {
				_ = enc.Encode(e)
			}
		}
		if cw != nil {
			cw.Flush()
		}
		if len(list) < f.Limit {
			return
		}
		last := list[len(list)-1].Record
		f.Before = &last
	}
}
