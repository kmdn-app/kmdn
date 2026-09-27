// Package admin implements the instance admin console APIs.
package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/audit"
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/mail"
	"github.com/kmdn-app/kmdn/internal/secrets"
	"github.com/kmdn-app/kmdn/internal/settings"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

// SMTP handles GET/PUT /admin/smtp and POST /admin/smtp/test.
type SMTP struct {
	DB      *store.DB
	Mail    *mail.Service
	Secrets *secrets.Store
	Log     *slog.Logger
}

func (h *SMTP) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin)
		r.Get("/admin/smtp", h.get)
		r.Put("/admin/smtp", h.put)
		r.Post("/admin/smtp/test", h.test)
	})
}

type smtpView struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Security    string `json:"security"`
	Username    string `json:"username"`
	From        string `json:"from"`
	HasPassword bool   `json:"has_password"`
	FromConfig  bool   `json:"from_config"`
	Configured  bool   `json:"configured"`
}

func (h *SMTP) get(w http.ResponseWriter, r *http.Request) {
	st, pw, err := h.Mail.Resolve(r.Context())
	if err != nil && !errors.Is(err, mail.ErrNotConfigured) {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, smtpView{Host: st.Host, Port: st.Port, Security: st.Security, Username: st.Username, From: st.From,
		HasPassword: pw != "", FromConfig: h.Mail.FromConfig(), Configured: err == nil})
}

type smtpInput struct {
	Host     string  `json:"host"`
	Port     int     `json:"port"`
	Security string  `json:"security"`
	Username string  `json:"username"`
	From     string  `json:"from"`
	Password *string `json:"password"` // nil keeps the stored password
}

func (in *smtpInput) validate() error {
	in.Host, in.From, in.Username = strings.TrimSpace(in.Host), strings.TrimSpace(in.From), strings.TrimSpace(in.Username)
	if in.Host == "" {
		return api.Invalid("host", "Enter the SMTP server host, e.g. smtp.postmarkapp.com.")
	}
	if in.Port <= 0 || in.Port > 65535 {
		return api.Invalid("port", "Enter a port between 1 and 65535 (usually 587).")
	}
	if in.Security == "" {
		in.Security = "starttls"
	}
	if in.Security != "starttls" && in.Security != "tls" && in.Security != "none" {
		return api.Invalid("security", "Security must be starttls, tls or none.")
	}
	if in.From == "" || !strings.Contains(in.From, "@") {
		return api.Invalid("from", "Enter the From address, e.g. Docs <docs@example.com>.")
	}
	return nil
}

func (h *SMTP) put(w http.ResponseWriter, r *http.Request) {
	if h.Mail.FromConfig() {
		api.Error(w, r, api.Err(http.StatusConflict, "smtp_locked", "SMTP is set in the kmdn config file or environment. Change it there."))
		return
	}
	var in smtpInput
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	if err := in.validate(); err != nil {
		api.Error(w, r, err)
		return
	}
	ctx := r.Context()
	err := h.DB.InTx(ctx, func(tx *store.Tx) error {
		var cur mail.SMTPSettings
		if err := settings.Get(ctx, tx, mail.SettingsKey, &cur); err != nil && !settings.IsNotFound(err) {
			return err
		}
		next := mail.SMTPSettings{Host: in.Host, Port: in.Port, Security: in.Security, Username: in.Username, From: in.From, PasswordSecretID: cur.PasswordSecretID}
		if in.Password != nil {
			var err error
			switch {
			case *in.Password == "" && cur.PasswordSecretID != "":
				err = h.Secrets.Delete(ctx, tx, cur.PasswordSecretID)
				next.PasswordSecretID = ""
			case *in.Password == "":
			case cur.PasswordSecretID != "":
				err = h.Secrets.Update(ctx, tx, cur.PasswordSecretID, []byte(*in.Password))
			default:
				next.PasswordSecretID, err = h.Secrets.Put(ctx, tx, "smtp_password", []byte(*in.Password))
			}
			if err != nil {
				return err
			}
		}
		if err := settings.Set(ctx, tx, mail.SettingsKey, next); err != nil {
			return err
		}
		p, _ := auth.FromContext(ctx)
		return audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID, Action: "smtp.updated", Data: map[string]any{"host": in.Host, "password_changed": in.Password != nil}})
	})
	if err != nil {
		api.Error(w, r, err)
		return
	}
	h.get(w, r)
}

func (h *SMTP) test(w http.ResponseWriter, r *http.Request) {
	var body struct {
		To       string     `json:"to"`
		Settings *smtpInput `json:"settings"` // test unsaved settings; nil uses saved ones
	}
	if err := api.Decode(r, &body); err != nil {
		api.Error(w, r, err)
		return
	}
	p, _ := auth.FromContext(r.Context())
	to := users.NormalizeEmail(body.To)
	if body.To == "" {
		to = p.User.Email
	}
	if to == "" {
		api.Error(w, r, api.Invalid("to", "Enter a valid address to send the test to."))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	msg := mail.Test(auth.InstanceName(ctx, h.DB), to)
	var err error
	if body.Settings != nil {
		if err := body.Settings.validate(); err != nil {
			api.Error(w, r, err)
			return
		}
		pw := ""
		if body.Settings.Password != nil {
			pw = *body.Settings.Password
		} else if _, saved, rerr := h.Mail.Resolve(ctx); rerr == nil {
			pw = saved
		}
		s := body.Settings
		err = mail.SendWith(ctx, mail.SMTPSettings{Host: s.Host, Port: s.Port, Security: s.Security, Username: s.Username, From: s.From}, pw, msg, h.Log)
	} else {
		err = h.Mail.Send(ctx, msg)
	}
	if err != nil {
		api.JSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "hint": "Check the host, port, security mode and credentials. Many providers need STARTTLS on port 587."})
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"ok": true, "to": to})
}
