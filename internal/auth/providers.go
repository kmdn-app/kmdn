package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/audit"
	"github.com/kmdn-app/kmdn/internal/events"
	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/orgs"
	"github.com/kmdn-app/kmdn/internal/settings"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

// Sign-in providers are how an embedding program adds ways to sign in
// (docs/specs/16-organizations.md#sign-in): a provider sends the browser to an
// identity provider and reads back who it vouches for; kmdn links that
// identity to an account and creates the session.

// Identity is a person an identity provider vouches for.
type Identity struct {
	// Issuer and Subject name the person at the identity provider (a SAML
	// entity ID and NameID, an OIDC issuer and sub). Together they are
	// stable and unique.
	Issuer  string
	Subject string
	// Email is used to find an existing account the first time, only when
	// the provider checked it.
	Email         string
	EmailVerified bool
	Name          string
	// OrgID is the org the provider signs people into, if any. With Create,
	// someone with no account gets one and joins that org (within its
	// member limit).
	OrgID  string
	Create bool
}

// ProviderInfo describes a provider.
type ProviderInfo struct {
	// ID appears in URLs and in the session's method (provider:<id>).
	ID   string `json:"id"`
	Name string `json:"name"`
	// Listed shows the provider on the sign-in page; others are reached
	// through their start URL (an org's "sign in with SSO" link).
	Listed bool `json:"-"`
}

// Provider is a way to sign in that kmdn doesn't ship.
type Provider interface {
	Info() ProviderInfo
	// Start sends the browser to the identity provider. state must come
	// back to Callback (OAuth state, SAML RelayState).
	Start(w http.ResponseWriter, r *http.Request, state string) error
	// Callback reads the identity provider's answer (GET or POST) and
	// returns who it vouches for and the state it was given.
	Callback(r *http.Request) (Identity, string, error)
}

// ProviderMethod is the session method for a provider.
func ProviderMethod(id string) string { return "provider:" + id }

// ProviderStartURL is where a sign-in with provider id starts.
func ProviderStartURL(id, redirect string) string {
	u := "/api/v1/auth/providers/" + id + "/start"
	if redirect != "" {
		u += "?redirect=" + url.QueryEscape(redirect)
	}
	return u
}

var providerID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// AddProvider registers p. IDs must be unique and URL-safe.
func (h *HTTP) AddProvider(p Provider) error {
	id := p.Info().ID
	if !providerID.MatchString(id) {
		return errors.New("auth: provider id must be lowercase letters, digits and dashes")
	}
	if h.providers == nil {
		h.providers = map[string]Provider{}
	}
	if _, dup := h.providers[id]; dup {
		return errors.New("auth: provider " + id + " registered twice")
	}
	h.providers[id] = p
	return nil
}

const providerCookie = "kmdn_signin"

type providerState struct {
	Provider string    `json:"provider"`
	Redirect string    `json:"redirect"`
	Created  time.Time `json:"created"`
}

func (h *HTTP) providerRoutes(r chi.Router) {
	r.Get("/auth/providers", h.listProviders)
	r.Get("/auth/providers/{id}/start", h.startProvider)
	r.Get("/auth/providers/{id}/callback", h.providerCallback)
	r.Post("/auth/providers/{id}/callback", h.providerCallback)
}

func (h *HTTP) listProviders(w http.ResponseWriter, _ *http.Request) {
	out := []map[string]string{}
	for _, p := range h.providers {
		if info := p.Info(); info.Listed {
			out = append(out, map[string]string{"id": info.ID, "name": info.Name, "start_url": ProviderStartURL(info.ID, "")})
		}
	}
	slices.SortFunc(out, func(a, b map[string]string) int { return strings.Compare(a["name"], b["name"]) })
	api.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func safePath(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.Contains(p, "\\") {
		return "/"
	}
	return p
}

func (h *HTTP) startProvider(w http.ResponseWriter, r *http.Request) {
	p, ok := h.providers[chi.URLParam(r, "id")]
	if !ok {
		http.Redirect(w, r, "/signin?oauth_error=unavailable", http.StatusFound)
		return
	}
	state := Token(24)
	st := providerState{Provider: p.Info().ID, Redirect: safePath(r.URL.Query().Get("redirect")), Created: time.Now()}
	if err := settings.Set(r.Context(), h.Svc.DB, "signin_state:"+Hash(state), st); err != nil {
		api.Error(w, r, err)
		return
	}
	// SAML answers with a cross-site POST, which a Lax cookie wouldn't
	// survive; the state is also bound server side and single-use.
	same := http.SameSiteLaxMode
	if h.Secure {
		same = http.SameSiteNoneMode
	}
	http.SetCookie(w, &http.Cookie{Name: h.CookieName(providerCookie), Value: state, Path: "/", HttpOnly: true, Secure: h.Secure, SameSite: same, MaxAge: 600})
	if err := p.Start(w, r, state); err != nil {
		http.Redirect(w, r, "/signin?oauth_error=unavailable", http.StatusFound)
	}
}

func (h *HTTP) providerCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, ok := h.providers[chi.URLParam(r, "id")]
	if !ok {
		http.Redirect(w, r, "/signin?oauth_error=unavailable", http.StatusFound)
		return
	}
	id, state, err := p.Callback(r)
	ck, ckErr := r.Cookie(h.CookieName(providerCookie))
	http.SetCookie(w, &http.Cookie{Name: h.CookieName(providerCookie), Value: "", Path: "/", MaxAge: -1, Secure: h.Secure})
	if err != nil {
		h.Svc.audit(ctx, audit.Entry{ActorType: audit.ActorSystem, IP: h.ip(r), Action: "auth.sign_in_failed", Data: map[string]any{"method": ProviderMethod(p.Info().ID)}})
		http.Redirect(w, r, "/signin?oauth_error=forge", http.StatusFound)
		return
	}
	var st providerState
	key := "signin_state:" + Hash(state)
	if ckErr != nil || state == "" || subtle.ConstantTimeCompare([]byte(ck.Value), []byte(state)) != 1 ||
		settings.Get(ctx, h.Svc.DB, key, &st) != nil || time.Since(st.Created) > 10*time.Minute || st.Provider != p.Info().ID {
		http.Redirect(w, r, "/signin?oauth_error=expired", http.StatusFound)
		return
	}
	_ = settings.Delete(ctx, h.Svc.DB, key)
	u, err := h.Svc.userForIdentity(ctx, p.Info().ID, id)
	if err != nil {
		code := "no_account"
		if errors.Is(err, errOrgFull) {
			code = "org_full"
		}
		http.Redirect(w, r, "/signin?oauth_error="+code, http.StatusFound)
		return
	}
	if err := h.SignIn(w, r, u, ProviderMethod(p.Info().ID)); err != nil {
		http.Redirect(w, r, "/signin?oauth_error="+SignInErrorCode(err), http.StatusFound)
		return
	}
	http.Redirect(w, r, st.Redirect, http.StatusFound)
}

var errOrgFull = errors.New("auth: the org is at its member limit")

// userForIdentity finds or makes the account an identity signs in: its link,
// else an active account with the same (verified) email, which gets linked,
// else a new account in the provider's org when it allows that.
func (s *Service) userForIdentity(ctx context.Context, provider string, id Identity) (users.User, error) {
	if id.Issuer == "" || id.Subject == "" {
		return users.User{}, errors.New("auth: identity without issuer or subject")
	}
	var u users.User
	err := s.DB.InTx(ctx, func(tx *store.Tx) error {
		var uid string
		err := store.QueryRow(ctx, tx, `SELECT user_id FROM identity_links WHERE issuer = ? AND subject = ?`, id.Issuer, id.Subject).Scan(&uid)
		if err == nil {
			if u, err = users.ByID(ctx, tx, uid); err != nil {
				return err
			}
			if u.Status != users.Active {
				return ErrDeactivated
			}
			_, err = store.Exec(ctx, tx, `UPDATE identity_links SET last_used_at = ?, email = ? WHERE issuer = ? AND subject = ?`, store.Millis(time.Now()), id.Email, id.Issuer, id.Subject)
			return err
		}
		if !errors.Is(store.NotFound(err), store.ErrNotFound) {
			return err
		}
		email := users.NormalizeEmail(id.Email)
		if !id.EmailVerified || email == "" {
			return ErrNoAccount
		}
		u, err = users.ByEmail(ctx, tx, email)
		switch {
		case err == nil && u.Status != users.Active:
			return ErrDeactivated
		case errors.Is(err, store.ErrNotFound):
			if !id.Create || id.OrgID == "" {
				return ErrNoAccount
			}
			if !s.seatFree(ctx, tx, id.OrgID, users.User{}) {
				return errOrgFull
			}
			if u, err = users.Create(ctx, tx, email, id.Name, false); err != nil {
				return err
			}
		case err != nil:
			return err
		}
		if id.OrgID != "" && id.Create {
			if in, err := orgs.Belongs(ctx, tx, id.OrgID, u); err != nil {
				return err
			} else if !in {
				if !s.seatFree(ctx, tx, id.OrgID, u) {
					return errOrgFull
				}
				if err := orgs.AddMember(ctx, tx, id.OrgID, u.ID, orgs.Member, ""); err != nil {
					return err
				}
				if err := audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorSystem, OrgID: id.OrgID, Action: "org.member_added", TargetType: "user", TargetID: u.ID, Data: map[string]any{"via": ProviderMethod(provider)}}); err != nil {
					return err
				}
				s.Events.Emit(ctx, events.Event{Type: events.MemberAdded, OrgID: id.OrgID, UserID: u.ID, Data: map[string]any{"via": ProviderMethod(provider)}})
			}
		}
		now := store.Millis(time.Now())
		if _, err := store.Exec(ctx, tx, `INSERT INTO identity_links (id, user_id, provider, issuer, subject, email, created_at, last_used_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			ids.New("idl"), u.ID, provider, id.Issuer, id.Subject, email, now, now); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorUser, ActorID: u.ID, Action: "account.identity_linked", Data: map[string]any{"provider": provider, "issuer": id.Issuer}})
	})
	return u, err
}
