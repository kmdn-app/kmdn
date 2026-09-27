// Package invites lets admins invite people by email, to the instance or to a
// repository with a role. Accepting an invite (the link proves control of the
// email address) creates the account if needed and signs the person in.
package invites

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/audit"
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/mail"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

// TTL is how long an invite link stays valid.
const TTL = 7 * 24 * time.Hour

// Service handles invites.
type Service struct {
	DB      *store.DB
	Mail    mail.Sender
	Auth    *auth.HTTP
	BaseURL string
}

// Invite is a pending or accepted invitation.
type Invite struct {
	ID         string      `json:"id"`
	Email      string      `json:"email"`
	InvitedBy  string      `json:"invited_by"`
	RepoID     string      `json:"repo_id,omitempty"`
	Role       access.Role `json:"role,omitempty"`
	CreatedAt  time.Time   `json:"created_at"`
	ExpiresAt  time.Time   `json:"expires_at"`
	AcceptedAt *time.Time  `json:"accepted_at,omitempty"`
}

// Result says what happened for an invited address.
type Result struct {
	Status string  `json:"status"` // invited | granted
	Email  string  `json:"email"`
	Invite *Invite `json:"invite,omitempty"`
}

// Create invites email. When an account already exists and a repo is given,
// the role is granted directly and no email is sent.
func (s *Service) Create(ctx context.Context, by auth.Principal, email string, repo *repos.Repo, role access.Role) (Result, error) {
	var res Result
	err := s.DB.InTx(ctx, func(tx *store.Tx) error {
		u, err := users.ByEmail(ctx, tx, email)
		if err == nil {
			if repo == nil {
				return api.Err(http.StatusConflict, "already_member", email+" already has an account.")
			}
			if err := access.Grant(ctx, tx, repo.ID, access.UserPrincipal, u.ID, role); err != nil {
				return err
			}
			res = Result{Status: "granted", Email: email}
			return audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorUser, ActorID: by.User.ID, Action: "member.role_changed", TargetType: "user", TargetID: u.ID, RepoID: repo.ID, Data: map[string]any{"role": role}})
		}
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		token := auth.Token(24)
		now := time.Now().UTC().Truncate(time.Millisecond)
		inv := Invite{ID: ids.New(ids.Invite), Email: email, InvitedBy: by.User.ID, Role: role, CreatedAt: now, ExpiresAt: now.Add(TTL)}
		var repoID, roleV any
		target := auth.InstanceName(ctx, tx)
		if repo != nil {
			inv.RepoID, repoID, roleV = repo.ID, repo.ID, string(role)
			target = repo.DisplayName
		}
		if _, err := store.Exec(ctx, tx, `INSERT INTO invites (id, email, invited_by, repo_id, role, token_hash, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			inv.ID, email, by.User.ID, repoID, roleV, auth.Hash(token), store.Millis(now), store.Millis(inv.ExpiresAt)); err != nil {
			return err
		}
		link := strings.TrimRight(s.BaseURL, "/") + "/invite/" + token
		if err := s.Mail.Send(ctx, mail.Invite(auth.InstanceName(ctx, tx), email, by.User.Name, target, link, int(TTL/(24*time.Hour)))); err != nil {
			return api.Err(http.StatusBadGateway, "mail_failed", "The invite couldn't be emailed: "+err.Error())
		}
		res = Result{Status: "invited", Email: email, Invite: &inv}
		return audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorUser, ActorID: by.User.ID, Action: "user.invited", TargetType: "invite", TargetID: inv.ID, RepoID: inv.RepoID, Data: map[string]any{"email": email}})
	})
	return res, err
}

// Details is what the accept page shows.
type Details struct {
	Email        string      `json:"email"`
	InviterName  string      `json:"inviter_name"`
	RepoName     string      `json:"repo_name,omitempty"`
	Role         access.Role `json:"role,omitempty"`
	ExpiresAt    time.Time   `json:"expires_at"`
	InstanceName string      `json:"instance_name"`
	HasAccount   bool        `json:"has_account"`
}

type row struct {
	id, email, inviter, repoID, role string
	exp                              int64
}

var errInvalid = api.Err(http.StatusNotFound, "invalid_invite", "This invite link is invalid, was already used, or has expired. Ask the person who invited you for a new one.")

func (s *Service) find(ctx context.Context, q store.Querier, token string) (row, error) {
	var r row
	var accepted *int64
	err := store.QueryRow(ctx, q, `SELECT id, email, COALESCE(invited_by, ''), COALESCE(repo_id, ''), COALESCE(role, ''), expires_at, accepted_at FROM invites WHERE token_hash = ?`, auth.Hash(token)).
		Scan(&r.id, &r.email, &r.inviter, &r.repoID, &r.role, &r.exp, &accepted)
	if err != nil || accepted != nil || store.FromMillis(r.exp).Before(time.Now()) {
		return r, errInvalid
	}
	return r, nil
}

// Routes registers invite endpoints.
func (s *Service) Routes(r chi.Router) {
	r.Get("/invites/{token}", s.get)
	r.Post("/invites/{token}/accept", s.accept)
	r.With(auth.RequireAdmin).Post("/admin/invites", s.adminInvite)
	r.With(auth.Require).Post("/repos/{repo}/invites", s.repoInvite)
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	inv, err := s.find(ctx, s.DB, chi.URLParam(r, "token"))
	if err != nil {
		api.Error(w, r, err)
		return
	}
	d := Details{Email: inv.email, Role: access.Role(inv.role), ExpiresAt: store.FromMillis(inv.exp), InstanceName: auth.InstanceName(ctx, s.DB)}
	if u, err := users.ByID(ctx, s.DB, inv.inviter); err == nil {
		d.InviterName = u.Name
	}
	if inv.repoID != "" {
		if rp, err := repos.Get(ctx, s.DB, inv.repoID); err == nil {
			d.RepoName = rp.DisplayName
		}
	}
	if _, err := users.ByEmail(ctx, s.DB, inv.email); err == nil {
		d.HasAccount = true
	}
	api.JSON(w, http.StatusOK, d)
}

func (s *Service) accept(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := api.Decode(r, &body); err != nil {
		api.Error(w, r, err)
		return
	}
	ctx := r.Context()
	var u users.User
	err := s.DB.InTx(ctx, func(tx *store.Tx) error {
		inv, err := s.find(ctx, tx, chi.URLParam(r, "token"))
		if err != nil {
			return err
		}
		u, err = users.ByEmail(ctx, tx, inv.email)
		switch {
		case errors.Is(err, store.ErrNotFound):
			name := strings.TrimSpace(body.Name)
			if name == "" {
				name = inv.email[:strings.IndexByte(inv.email, '@')]
			}
			if u, err = users.Create(ctx, tx, inv.email, name, false); err != nil {
				return err
			}
		case err != nil:
			return err
		case u.Status != users.Active:
			return api.Err(http.StatusForbidden, "deactivated", "This account is deactivated. Contact an admin.")
		}
		if inv.repoID != "" {
			role, err := access.Parse(inv.role)
			if err != nil {
				role = access.Viewer
			}
			if cur, _ := access.Effective(ctx, tx, u, inv.repoID); !cur.AtLeast(role) {
				if err := access.Grant(ctx, tx, inv.repoID, access.UserPrincipal, u.ID, role); err != nil {
					return err
				}
			}
		}
		if _, err := store.Exec(ctx, tx, `UPDATE invites SET accepted_at = ? WHERE id = ?`, store.Millis(time.Now()), inv.id); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorUser, ActorID: u.ID, Action: "invite.accepted", TargetType: "invite", TargetID: inv.id, RepoID: inv.repoID})
	})
	if err != nil {
		api.Error(w, r, err)
		return
	}
	if err := s.Auth.SignIn(w, r, u, "invite"); err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, u)
}

type inviteBody struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

func (s *Service) adminInvite(w http.ResponseWriter, r *http.Request) {
	var body struct {
		inviteBody
		RepoID string `json:"repo_id"`
	}
	if err := api.Decode(r, &body); err != nil {
		api.Error(w, r, err)
		return
	}
	var repo *repos.Repo
	if body.RepoID != "" {
		rp, err := repos.Get(r.Context(), s.DB, body.RepoID)
		if err != nil {
			api.Error(w, r, api.Invalid("repo_id", "No such repository."))
			return
		}
		repo = &rp
	}
	s.create(w, r, body.inviteBody, repo)
}

func (s *Service) repoInvite(w http.ResponseWriter, r *http.Request) {
	var body inviteBody
	if err := api.Decode(r, &body); err != nil {
		api.Error(w, r, err)
		return
	}
	p, _ := auth.FromContext(r.Context())
	rp, err := repos.Get(r.Context(), s.DB, chi.URLParam(r, "repo"))
	if err != nil {
		api.Error(w, r, api.ErrNotFound)
		return
	}
	if role, err := access.Effective(r.Context(), s.DB, p.User, rp.ID); err != nil || !role.AtLeast(access.Admin) {
		api.Error(w, r, api.Err(http.StatusForbidden, "forbidden", "Only repository admins can invite people."))
		return
	}
	s.create(w, r, body, &rp)
}

func (s *Service) create(w http.ResponseWriter, r *http.Request, body inviteBody, repo *repos.Repo) {
	email := users.NormalizeEmail(body.Email)
	if email == "" {
		api.Error(w, r, api.Invalid("email", "Enter a valid email address."))
		return
	}
	role := access.None
	if repo != nil {
		var err error
		if role, err = access.Parse(body.Role); err != nil {
			api.Error(w, r, api.Invalid("role", err.Error()))
			return
		}
	}
	p, _ := auth.FromContext(r.Context())
	res, err := s.Create(r.Context(), p, email, repo, role)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusCreated, res)
}
