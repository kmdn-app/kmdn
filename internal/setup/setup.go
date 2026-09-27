// Package setup runs the first-run wizard: while no instance admin exists, a
// one-time setup token is printed in the server log, and whoever holds it can
// create the first admin. See docs/specs/13-operations.md#first-run-setup-wizard.
package setup

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/audit"
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/mail"
	"github.com/kmdn-app/kmdn/internal/orgs"
	"github.com/kmdn-app/kmdn/internal/settings"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

const (
	tokenKey     = "setup_token_hash"
	completedKey = "setup_completed"
)

// Service coordinates setup state.
type Service struct {
	DB      *store.DB
	Mail    *mail.Service
	Auth    *auth.HTTP
	BaseURL string
	Log     *slog.Logger
}

// Prepare issues a fresh setup token when no admin exists and logs the URL.
// It returns the token ("" when setup is not needed).
func (s *Service) Prepare(ctx context.Context) (string, error) {
	n, err := users.CountAdmins(ctx, s.DB)
	if err != nil || n > 0 {
		return "", err
	}
	token := auth.Token(24)
	if err := settings.Set(ctx, s.DB, tokenKey, auth.Hash(token)); err != nil {
		return "", err
	}
	s.Log.Warn("kmdn needs setup: open this URL to create the first admin (the link changes on every restart until setup is done)",
		"url", strings.TrimRight(s.BaseURL, "/")+"/setup?token="+token)
	return token, nil
}

// Status is returned by GET /setup/status (public).
type Status struct {
	Needed         bool   `json:"needed"`
	AdminExists    bool   `json:"admin_exists"`
	Completed      bool   `json:"completed"`
	SMTPConfigured bool   `json:"smtp_configured"`
	SMTPFromConfig bool   `json:"smtp_from_config"`
	InstanceName   string `json:"instance_name"`
}

func (s *Service) status(ctx context.Context) (Status, error) {
	n, err := users.CountAdmins(ctx, s.DB)
	if err != nil {
		return Status{}, err
	}
	var completed bool
	if err := settings.Get(ctx, s.DB, completedKey, &completed); err != nil && !settings.IsNotFound(err) {
		return Status{}, err
	}
	return Status{
		Needed:         n == 0 || !completed,
		AdminExists:    n > 0,
		Completed:      completed,
		SMTPConfigured: s.Mail.Configured(ctx),
		SMTPFromConfig: s.Mail.FromConfig(),
		InstanceName:   auth.InstanceName(ctx, s.DB),
	}, nil
}

// Routes registers the setup endpoints.
func (s *Service) Routes(r chi.Router) {
	r.Get("/setup/status", s.getStatus)
	r.Post("/setup/admin", s.createAdmin)
	r.With(auth.RequireAdmin).Post("/setup/complete", s.complete)
}

func (s *Service) getStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.status(r.Context())
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, st)
}

var errBadToken = api.Err(http.StatusForbidden, "invalid_setup_token", "The setup link is invalid. Use the newest link printed in the kmdn server log.")

func (s *Service) createAdmin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token        string `json:"token"`
		Name         string `json:"name"`
		Email        string `json:"email"`
		InstanceName string `json:"instance_name"`
	}
	if err := api.Decode(r, &body); err != nil {
		api.Error(w, r, err)
		return
	}
	name, email := strings.TrimSpace(body.Name), users.NormalizeEmail(body.Email)
	if name == "" || len(name) > 120 {
		api.Error(w, r, api.Invalid("name", "Enter your name."))
		return
	}
	if email == "" {
		api.Error(w, r, api.Invalid("email", "Enter a valid email address. You'll use it to sign in."))
		return
	}
	ctx := r.Context()
	var u users.User
	err := s.DB.InTx(ctx, func(tx *store.Tx) error {
		n, err := users.CountAdmins(ctx, tx)
		if err != nil {
			return err
		}
		if n > 0 {
			return api.Err(http.StatusConflict, "setup_done", "An admin already exists. Sign in instead.")
		}
		var want string
		if err := settings.Get(ctx, tx, tokenKey, &want); err != nil {
			if settings.IsNotFound(err) {
				return errBadToken
			}
			return err
		}
		if body.Token == "" || subtle.ConstantTimeCompare([]byte(auth.Hash(body.Token)), []byte(want)) != 1 {
			return errBadToken
		}
		u, err = users.ByEmail(ctx, tx, email)
		switch {
		case errors.Is(err, store.ErrNotFound):
			if u, err = users.Create(ctx, tx, email, name, true); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			if _, err := store.Exec(ctx, tx, `UPDATE users SET is_instance_admin = ?, status = ?, name = ? WHERE id = ?`, true, users.Active, name, u.ID); err != nil {
				return err
			}
			u.IsInstanceAdmin, u.Name = true, name
		}
		if in := strings.TrimSpace(body.InstanceName); in != "" {
			if err := settings.Set(ctx, tx, "instance_name", in); err != nil {
				return err
			}
			// The default org takes the name, and a matching address while
			// it still has the placeholder one.
			if err := orgs.Rename(ctx, tx, orgs.DefaultID, in); err != nil {
				return err
			}
			if o, err := orgs.ByID(ctx, tx, orgs.DefaultID); err == nil && o.Slug == "default" {
				if err := orgs.SetSlug(ctx, tx, orgs.DefaultID, orgs.Slugify(in)); err != nil && !errors.Is(err, orgs.ErrSlugTaken) {
					return err
				}
			}
		}
		if err := orgs.AddMember(ctx, tx, orgs.DefaultID, u.ID, orgs.Owner, ""); err != nil {
			return err
		}
		if err := settings.Delete(ctx, tx, tokenKey); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorUser, ActorID: u.ID, IP: api.ClientIP(r, s.Auth.TrustedProxies), Action: "setup.admin_created", TargetType: "user", TargetID: u.ID})
	})
	if err != nil {
		api.Error(w, r, err)
		return
	}
	if err := s.Auth.SignIn(w, r, u, "setup"); err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusCreated, u)
}

func (s *Service) complete(w http.ResponseWriter, r *http.Request) {
	if !s.Mail.Configured(r.Context()) {
		api.Error(w, r, api.Err(http.StatusConflict, "smtp_required", "Set up email (SMTP) first: kmdn signs people in with email links."))
		return
	}
	if err := settings.Set(r.Context(), s.DB, completedKey, true); err != nil {
		api.Error(w, r, err)
		return
	}
	p, _ := auth.FromContext(r.Context())
	_ = audit.Write(r.Context(), s.DB, audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID, Action: "setup.completed"})
	s.getStatus(w, r)
}
