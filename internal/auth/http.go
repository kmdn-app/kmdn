package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/audit"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

// Cookie names.
const (
	SessionCookie = "kmdn_session"
	CSRFCookie    = "kmdn_csrf"
	CSRFHeader    = "X-Kmdn-CSRF"
)

type ctxKey int

const principalKey ctxKey = iota

// Principal is the authenticated caller.
type Principal struct {
	User    users.User
	Session Session
}

// FromContext returns the caller, if signed in.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey).(Principal)
	return p, ok
}

// WithPrincipal stores p in ctx (used by tests and the WebSocket layer).
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey, p)
}

// HTTP exposes the auth endpoints and middleware.
type HTTP struct {
	Svc            *Service
	Secure         bool // set cookies with Secure (base_url is https)
	TrustedProxies []*net.IPNet

	requestByEmail *api.Limiter
	requestByIP    *api.Limiter
	verifyByIP     *api.Limiter
}

// NewHTTP builds the handler set with default rate limits.
func NewHTTP(svc *Service, secure bool, trusted []*net.IPNet) *HTTP {
	return &HTTP{
		Svc: svc, Secure: secure, TrustedProxies: trusted,
		requestByEmail: api.NewLimiter(5, 15*time.Minute),
		requestByIP:    api.NewLimiter(20, 15*time.Minute),
		verifyByIP:     api.NewLimiter(30, 15*time.Minute),
	}
}

func (h *HTTP) ip(r *http.Request) string { return api.ClientIP(r, h.TrustedProxies) }

// Middleware attaches the Principal when a valid session cookie is present
// and enforces CSRF on unsafe methods for cookie-authenticated requests.
func (h *HTTP) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(SessionCookie)
		if err != nil || c.Value == "" {
			next.ServeHTTP(w, r)
			return
		}
		sess, u, err := h.Svc.Lookup(r.Context(), c.Value)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) && !errors.Is(err, ErrDeactivated) {
				api.Error(w, r, err)
				return
			}
			h.clearCookies(w)
			next.ServeHTTP(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			got := r.Header.Get(CSRFHeader)
			if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(sess.CSRF)) != 1 {
				api.Error(w, r, api.ErrCSRF)
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), Principal{User: u, Session: sess})))
	})
}

// Require rejects anonymous requests.
func Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := FromContext(r.Context()); !ok {
			api.Error(w, r, api.ErrUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAdmin rejects callers who are not instance admins.
func RequireAdmin(next http.Handler) http.Handler {
	return Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := FromContext(r.Context())
		if !p.User.IsInstanceAdmin {
			api.Error(w, r, api.ErrForbidden)
			return
		}
		next.ServeHTTP(w, r)
	}))
}

// SetSessionCookies writes the session and CSRF cookies.
func (h *HTTP) SetSessionCookies(w http.ResponseWriter, token string, sess Session) {
	maxAge := int(time.Until(sess.ExpiresAt).Seconds())
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: h.Secure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
	http.SetCookie(w, &http.Cookie{Name: CSRFCookie, Value: sess.CSRF, Path: "/", HttpOnly: false, Secure: h.Secure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}

func (h *HTTP) clearCookies(w http.ResponseWriter) {
	for _, n := range []string{SessionCookie, CSRFCookie} {
		http.SetCookie(w, &http.Cookie{Name: n, Value: "", Path: "/", MaxAge: -1, HttpOnly: n == SessionCookie, Secure: h.Secure, SameSite: http.SameSiteLaxMode})
	}
}

// SignIn creates a session for u and writes cookies.
func (h *HTTP) SignIn(w http.ResponseWriter, r *http.Request, u users.User, method string) error {
	token, sess, err := h.Svc.CreateSession(r.Context(), h.Svc.DB, u.ID, h.ip(r), r.UserAgent())
	if err != nil {
		return err
	}
	h.SetSessionCookies(w, token, sess)
	h.Svc.audit(r.Context(), audit.Entry{ActorType: audit.ActorUser, ActorID: u.ID, IP: h.ip(r), Action: "auth.sign_in", Data: map[string]any{"method": method}})
	return nil
}

// Routes registers /auth and /me under the API router.
func (h *HTTP) Routes(r chi.Router) {
	r.Post("/auth/magic-link", h.requestLink)
	r.Post("/auth/magic-link/verify", h.verify)
	r.Post("/auth/logout", h.logout)
	r.Group(func(r chi.Router) {
		r.Use(Require)
		r.Get("/me", h.me)
		r.Patch("/me", h.updateMe)
		r.Get("/me/sessions", h.sessions)
		r.Delete("/me/sessions/{id}", h.revokeSession)
	})
}

type meResponse struct {
	users.User
	CSRF string `json:"csrf_token"`
}

func (h *HTTP) me(w http.ResponseWriter, r *http.Request) {
	p, _ := FromContext(r.Context())
	api.JSON(w, http.StatusOK, meResponse{User: p.User, CSRF: p.Session.CSRF})
}

func (h *HTTP) updateMe(w http.ResponseWriter, r *http.Request) {
	p, _ := FromContext(r.Context())
	var body struct {
		Name  *string `json:"name"`
		Theme *string `json:"theme"`
	}
	if err := api.Decode(r, &body); err != nil {
		api.Error(w, r, err)
		return
	}
	name, theme := p.User.Name, p.User.Theme
	if body.Name != nil {
		name = strings.TrimSpace(*body.Name)
		if name == "" || len(name) > 120 {
			api.Error(w, r, api.Invalid("name", "Enter a name between 1 and 120 characters."))
			return
		}
	}
	if body.Theme != nil {
		switch *body.Theme {
		case "system", "light", "dark":
			theme = *body.Theme
		default:
			api.Error(w, r, api.Invalid("theme", "Theme must be system, light or dark."))
			return
		}
	}
	if err := users.UpdateProfile(r.Context(), h.Svc.DB, p.User.ID, name, theme); err != nil {
		api.Error(w, r, err)
		return
	}
	u, err := users.ByID(r.Context(), h.Svc.DB, p.User.ID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, meResponse{User: u, CSRF: p.Session.CSRF})
}

func (h *HTTP) requestLink(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	if err := api.Decode(r, &body); err != nil {
		api.Error(w, r, err)
		return
	}
	email := users.NormalizeEmail(body.Email)
	if email == "" {
		api.Error(w, r, api.Invalid("email", "Enter a valid email address, like name@company.com."))
		return
	}
	if !h.requestByIP.Allow(h.ip(r)) || !h.requestByEmail.Allow(email) {
		api.Error(w, r, api.ErrRateLimited)
		return
	}
	if _, err := h.Svc.RequestLink(r.Context(), email, h.ip(r)); err != nil {
		api.Error(w, r, err)
		return
	}
	// Same answer whether or not the account exists.
	api.JSON(w, http.StatusAccepted, map[string]any{"status": "sent", "expires_in_minutes": int(LinkTTL / time.Minute)})
}

func (h *HTTP) verify(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if err := api.Decode(r, &body); err != nil {
		api.Error(w, r, err)
		return
	}
	if !h.verifyByIP.Allow(h.ip(r)) {
		api.Error(w, r, api.ErrRateLimited)
		return
	}
	var (
		u      users.User
		err    error
		method string
	)
	switch {
	case body.Token != "":
		method = "magic_link"
		u, err = h.Svc.VerifyToken(r.Context(), body.Token)
	case body.Email != "" && body.Code != "":
		method = "magic_code"
		u, err = h.Svc.VerifyCode(r.Context(), users.NormalizeEmail(body.Email), body.Code)
	default:
		api.Error(w, r, api.Err(http.StatusBadRequest, "invalid_body", "Send either token, or email and code."))
		return
	}
	if err != nil {
		if errors.Is(err, ErrInvalidLink) || errors.Is(err, ErrNoAccount) || errors.Is(err, ErrDeactivated) {
			h.Svc.audit(r.Context(), audit.Entry{ActorType: audit.ActorSystem, IP: h.ip(r), Action: "auth.sign_in_failed", Data: map[string]any{"method": method}})
			api.Error(w, r, api.Err(http.StatusUnauthorized, "invalid_link", "This sign-in link or code is invalid or has expired. Request a new one."))
			return
		}
		api.Error(w, r, err)
		return
	}
	if err := h.SignIn(w, r, u, method); err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, u)
}

func (h *HTTP) logout(w http.ResponseWriter, r *http.Request) {
	if p, ok := FromContext(r.Context()); ok {
		if err := h.Svc.Revoke(r.Context(), p.Session.ID); err != nil {
			api.Error(w, r, err)
			return
		}
	}
	h.clearCookies(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h *HTTP) sessions(w http.ResponseWriter, r *http.Request) {
	p, _ := FromContext(r.Context())
	list, err := h.Svc.Sessions(r.Context(), p.User.ID, p.Session.ID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	if list == nil {
		list = []Session{}
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": list})
}

func (h *HTTP) revokeSession(w http.ResponseWriter, r *http.Request) {
	p, _ := FromContext(r.Context())
	id := chi.URLParam(r, "id")
	list, err := h.Svc.Sessions(r.Context(), p.User.ID, p.Session.ID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	for _, s := range list {
		if s.ID == id {
			if err := h.Svc.Revoke(r.Context(), id); err != nil {
				api.Error(w, r, err)
				return
			}
			if id == p.Session.ID {
				h.clearCookies(w)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	api.Error(w, r, api.ErrNotFound)
}
