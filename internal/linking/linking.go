// Package linking links GitHub and GitLab accounts to kmdn users over OAuth,
// and signs people in with a linked account. It never creates accounts on its
// own except for auto-join email domains. See
// docs/specs/09-auth-permissions.md#linked-accounts.
package linking

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/audit"
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/forge"
	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/secrets"
	"github.com/kmdn-app/kmdn/internal/settings"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

const stateCookie = "kmdn_oauth"

// Service implements the OAuth flows.
type Service struct {
	DB      *store.DB
	Secrets *secrets.Store
	AuthH   *auth.HTTP
	BaseURL string
	HTTP    *http.Client
}

func (s *Service) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

// Account is a linked forge identity.
type Account struct {
	ID           string    `json:"id"`
	ForgeHostID  string    `json:"forge_host_id"`
	ForgeKind    string    `json:"forge_kind"`
	HostName     string    `json:"host_name"`
	ForgeUserID  string    `json:"forge_user_id"`
	Login        string    `json:"login"`
	NoreplyEmail string    `json:"noreply_email"`
	AvatarURL    string    `json:"avatar_url"`
	LinkedAt     time.Time `json:"linked_at"`
}

// Accounts lists a user's linked accounts.
func Accounts(ctx context.Context, q store.Querier, userID string) ([]Account, error) {
	rows, err := store.Query(ctx, q, `SELECT a.id, a.forge_host_id, h.kind, h.display_name, a.forge_user_id, a.login, a.noreply_email, a.avatar_url, a.linked_at
		FROM linked_accounts a JOIN forge_hosts h ON h.id = a.forge_host_id WHERE a.user_id = ? ORDER BY a.linked_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Account{}
	for rows.Next() {
		var a Account
		var at int64
		if err := rows.Scan(&a.ID, &a.ForgeHostID, &a.ForgeKind, &a.HostName, &a.ForgeUserID, &a.Login, &a.NoreplyEmail, &a.AvatarURL, &at); err != nil {
			return nil, err
		}
		a.LinkedAt = store.FromMillis(at)
		out = append(out, a)
	}
	return out, rows.Err()
}

// Routes registers the OAuth and linked-account endpoints.
func (s *Service) Routes(r chi.Router) {
	r.Get("/auth/oauth/providers", s.providers)
	r.Get("/auth/oauth/{host}/start", s.start)
	r.Get("/auth/oauth/{host}/callback", s.callback)
	r.Group(func(r chi.Router) {
		r.Use(auth.Require)
		r.Get("/me/linked-accounts", s.listLinked)
		r.Delete("/me/linked-accounts/{id}", s.unlink)
		r.Put("/me/commit-email", s.setCommitEmail)
	})
}

type provider struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	DisplayName string `json:"display_name"`
}

func (s *Service) oauthHosts(ctx context.Context) ([]repos.HostRecord, error) {
	all, err := repos.ListHosts(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	var out []repos.HostRecord
	for _, h := range all {
		if (h.Kind == forge.KindGitHub || h.Kind == forge.KindGitLab) && h.ClientID != "" && h.ClientSecretRef != "" {
			out = append(out, h)
		}
	}
	return out, nil
}

func (s *Service) providers(w http.ResponseWriter, r *http.Request) {
	hs, err := s.oauthHosts(r.Context())
	if err != nil {
		api.Error(w, r, err)
		return
	}
	out := []provider{}
	for _, h := range hs {
		out = append(out, provider{ID: h.ID, Kind: h.Kind, DisplayName: h.DisplayName})
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": out})
}

type pending struct {
	HostID   string    `json:"host_id"`
	Mode     string    `json:"mode"` // link | signin
	UserID   string    `json:"user_id,omitempty"`
	Redirect string    `json:"redirect"`
	Created  time.Time `json:"created"`
}

func (s *Service) redirectURI(hostID string) string {
	return strings.TrimRight(s.BaseURL, "/") + "/api/v1/auth/oauth/" + hostID + "/callback"
}

func safeRedirect(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.Contains(p, "\\") {
		return "/"
	}
	return p
}

func (s *Service) start(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	h, err := repos.GetHost(ctx, s.DB, chi.URLParam(r, "host"))
	if err != nil || h.ClientID == "" {
		http.Redirect(w, r, "/signin?oauth_error=unavailable", http.StatusFound)
		return
	}
	mode := r.URL.Query().Get("mode")
	st := pending{HostID: h.ID, Mode: "signin", Redirect: safeRedirect(r.URL.Query().Get("redirect")), Created: time.Now()}
	if mode == "link" {
		p, ok := auth.FromContext(ctx)
		if !ok {
			http.Redirect(w, r, "/signin", http.StatusFound)
			return
		}
		st.Mode, st.UserID = "link", p.User.ID
	}
	state := auth.Token(24)
	if err := settings.Set(ctx, s.DB, "oauth_state:"+auth.Hash(state), st); err != nil {
		api.Error(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Value: state, Path: "/api/v1/auth/oauth/", HttpOnly: true, Secure: s.AuthH.Secure, SameSite: http.SameSiteLaxMode, MaxAge: 600})
	var authURL string
	q := url.Values{"client_id": {h.ClientID}, "redirect_uri": {s.redirectURI(h.ID)}, "state": {state}}
	switch h.Kind {
	case forge.KindGitHub:
		authURL = strings.TrimRight(h.BaseURL, "/") + "/login/oauth/authorize?" + q.Encode()
	case forge.KindGitLab:
		q.Set("response_type", "code")
		q.Set("scope", "read_user")
		authURL = strings.TrimRight(h.BaseURL, "/") + "/oauth/authorize?" + q.Encode()
	default:
		http.Redirect(w, r, "/signin?oauth_error=unavailable", http.StatusFound)
		return
	}
	http.Redirect(w, r, authURL, http.StatusFound)
}

// Profile is what we learn from the forge.
type Profile struct {
	ID           string
	Login        string
	Name         string
	AvatarURL    string
	VerifiedMail []string // verified addresses, primary first
	Noreply      string
}

func fail(w http.ResponseWriter, r *http.Request, st pending, code string) {
	target := "/signin?oauth_error=" + code
	if st.Mode == "link" {
		target = safeRedirect(st.Redirect)
		sep := "?"
		if strings.Contains(target, "?") {
			sep = "&"
		}
		target += sep + "link_error=" + code
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func (s *Service) callback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	state := r.URL.Query().Get("state")
	ck, err := r.Cookie(stateCookie)
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Value: "", Path: "/api/v1/auth/oauth/", MaxAge: -1})
	var st pending
	key := "oauth_state:" + auth.Hash(state)
	if err != nil || state == "" || subtle.ConstantTimeCompare([]byte(ck.Value), []byte(state)) != 1 || settings.Get(ctx, s.DB, key, &st) != nil || time.Since(st.Created) > 10*time.Minute {
		http.Redirect(w, r, "/signin?oauth_error=expired", http.StatusFound)
		return
	}
	_ = settings.Delete(ctx, s.DB, key)
	if st.HostID != chi.URLParam(r, "host") {
		fail(w, r, st, "expired")
		return
	}
	h, err := repos.GetHost(ctx, s.DB, st.HostID)
	if err != nil {
		fail(w, r, st, "unavailable")
		return
	}
	prof, err := s.exchange(ctx, h, r.URL.Query().Get("code"))
	if err != nil {
		fail(w, r, st, "forge")
		return
	}
	switch st.Mode {
	case "link":
		p, ok := auth.FromContext(ctx)
		if !ok || p.User.ID != st.UserID {
			fail(w, r, st, "session")
			return
		}
		if err := s.link(ctx, p.User, h, prof); err != nil {
			fail(w, r, st, "taken")
			return
		}
		http.Redirect(w, r, safeRedirect(st.Redirect), http.StatusFound)
	default:
		u, err := s.userFor(ctx, h, prof)
		if err != nil {
			fail(w, r, st, "no_account")
			return
		}
		if err := s.AuthH.SignIn(w, r, u, "oauth_"+h.Kind); err != nil {
			fail(w, r, st, "session")
			return
		}
		http.Redirect(w, r, safeRedirect(st.Redirect), http.StatusFound)
	}
}

var errTaken = errors.New("linking: forge account linked to another user")

func (s *Service) link(ctx context.Context, u users.User, h repos.HostRecord, p Profile) error {
	return s.DB.InTx(ctx, func(tx *store.Tx) error {
		var owner string
		err := store.QueryRow(ctx, tx, `SELECT user_id FROM linked_accounts WHERE forge_host_id = ? AND forge_user_id = ?`, h.ID, p.ID).Scan(&owner)
		switch {
		case err == nil && owner != u.ID:
			return errTaken
		case err == nil:
			_, err = store.Exec(ctx, tx, `UPDATE linked_accounts SET login = ?, emails = ?, noreply_email = ?, avatar_url = ?, linked_at = ? WHERE forge_host_id = ? AND forge_user_id = ?`,
				p.Login, mustJSON(p.VerifiedMail), p.Noreply, p.AvatarURL, store.Millis(time.Now()), h.ID, p.ID)
			return err
		}
		if _, err := store.Exec(ctx, tx, `INSERT INTO linked_accounts (id, user_id, forge_host_id, forge_user_id, login, emails, noreply_email, avatar_url, linked_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			ids.New("la"), u.ID, h.ID, p.ID, p.Login, mustJSON(p.VerifiedMail), p.Noreply, p.AvatarURL, store.Millis(time.Now())); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorUser, ActorID: u.ID, Action: "account.linked", TargetType: "forge_host", TargetID: h.ID, Data: map[string]any{"login": p.Login}})
	})
}

// userFor resolves the kmdn user for a sign-in: an existing link, or an
// existing account with a matching verified email (which then gets linked).
func (s *Service) userFor(ctx context.Context, h repos.HostRecord, p Profile) (users.User, error) {
	var uid string
	if err := store.QueryRow(ctx, s.DB, `SELECT user_id FROM linked_accounts WHERE forge_host_id = ? AND forge_user_id = ?`, h.ID, p.ID).Scan(&uid); err == nil {
		u, err := users.ByID(ctx, s.DB, uid)
		if err != nil || u.Status != users.Active {
			return u, errors.New("linking: account inactive")
		}
		return u, nil
	}
	for _, e := range p.VerifiedMail {
		u, err := users.ByEmail(ctx, s.DB, strings.ToLower(e))
		if err == nil && u.Status == users.Active {
			return u, s.link(ctx, u, h, p)
		}
	}
	return users.User{}, errors.New("linking: no account")
}

func (s *Service) exchange(ctx context.Context, h repos.HostRecord, code string) (Profile, error) {
	if code == "" {
		return Profile{}, errors.New("linking: no code")
	}
	secret, err := s.Secrets.Get(ctx, s.DB, h.ClientSecretRef)
	if err != nil {
		return Profile{}, err
	}
	form := url.Values{"client_id": {h.ClientID}, "client_secret": {string(secret)}, "code": {code}, "redirect_uri": {s.redirectURI(h.ID)}}
	tokenURL := strings.TrimRight(h.BaseURL, "/") + "/login/oauth/access_token"
	if h.Kind == forge.KindGitLab {
		form.Set("grant_type", "authorization_code")
		tokenURL = strings.TrimRight(h.BaseURL, "/") + "/oauth/token"
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	var tok struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := s.doJSON(req, &tok); err != nil {
		return Profile{}, err
	}
	if tok.AccessToken == "" {
		return Profile{}, fmt.Errorf("linking: token exchange failed: %s", tok.Error)
	}
	host := hostOf(h.BaseURL)
	switch h.Kind {
	case forge.KindGitHub:
		var u struct {
			ID        int64  `json:"id"`
			Login     string `json:"login"`
			Name      string `json:"name"`
			AvatarURL string `json:"avatar_url"`
		}
		if err := s.get(ctx, h.APIURL+"/user", "Bearer "+tok.AccessToken, &u); err != nil {
			return Profile{}, err
		}
		var emails []struct {
			Email    string `json:"email"`
			Primary  bool   `json:"primary"`
			Verified bool   `json:"verified"`
		}
		_ = s.get(ctx, h.APIURL+"/user/emails", "Bearer "+tok.AccessToken, &emails)
		p := Profile{ID: strconv.FormatInt(u.ID, 10), Login: u.Login, Name: u.Name, AvatarURL: u.AvatarURL, Noreply: GitHubNoreply(host, u.ID, u.Login)}
		for _, e := range emails {
			if e.Verified && e.Primary {
				p.VerifiedMail = append([]string{e.Email}, p.VerifiedMail...)
			} else if e.Verified {
				p.VerifiedMail = append(p.VerifiedMail, e.Email)
			}
		}
		return p, nil
	case forge.KindGitLab:
		var u struct {
			ID          int64  `json:"id"`
			Username    string `json:"username"`
			Name        string `json:"name"`
			Email       string `json:"email"`
			AvatarURL   string `json:"avatar_url"`
			ConfirmedAt string `json:"confirmed_at"`
		}
		if err := s.get(ctx, strings.TrimRight(h.BaseURL, "/")+"/api/v4/user", "Bearer "+tok.AccessToken, &u); err != nil {
			return Profile{}, err
		}
		p := Profile{ID: strconv.FormatInt(u.ID, 10), Login: u.Username, Name: u.Name, AvatarURL: u.AvatarURL, Noreply: GitLabNoreply(host, u.ID, u.Username)}
		if u.Email != "" && u.ConfirmedAt != "" {
			p.VerifiedMail = []string{u.Email}
		}
		return p, nil
	}
	return Profile{}, errors.New("linking: unsupported forge")
}

func (s *Service) get(ctx context.Context, u, auth string, out any) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/json")
	return s.doJSON(req, out)
}

func (s *Service) doJSON(req *http.Request, out any) error {
	res, err := s.client().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return fmt.Errorf("linking: %s %d", req.URL.Path, res.StatusCode)
	}
	return json.Unmarshal(b, out)
}

func hostOf(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	return u.Host
}

// GitHubNoreply returns the address GitHub attributes to the account.
func GitHubNoreply(host string, id int64, login string) string {
	if host == "" || host == "github.com" {
		return fmt.Sprintf("%d+%s@users.noreply.github.com", id, login)
	}
	return fmt.Sprintf("%d+%s@users.noreply.%s", id, login, host)
}

// GitLabNoreply returns GitLab's private commit email for the account.
func GitLabNoreply(host string, id int64, username string) string {
	if host == "" {
		host = "gitlab.com"
	}
	return fmt.Sprintf("%d-%s@users.noreply.%s", id, username, host)
}

func mustJSON(v any) string {
	if v == nil {
		return "[]"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func (s *Service) listLinked(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	list, err := Accounts(r.Context(), s.DB, p.User.ID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *Service) unlink(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	res, err := store.Exec(r.Context(), s.DB, `DELETE FROM linked_accounts WHERE id = ? AND user_id = ?`, chi.URLParam(r, "id"), p.User.ID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		api.Error(w, r, api.ErrNotFound)
		return
	}
	_ = audit.Write(r.Context(), s.DB, audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID, Action: "account.unlinked"})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) setCommitEmail(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode   string `json:"mode"`
		Custom string `json:"custom"`
	}
	if err := api.Decode(r, &body); err != nil {
		api.Error(w, r, err)
		return
	}
	p, _ := auth.FromContext(r.Context())
	var custom any
	switch body.Mode {
	case "forge_noreply", "account":
	case "custom":
		e := users.NormalizeEmail(body.Custom)
		if e == "" {
			api.Error(w, r, api.Invalid("custom", "Enter a valid email address."))
			return
		}
		custom = e
	default:
		api.Error(w, r, api.Invalid("mode", "Mode must be forge_noreply, account or custom."))
		return
	}
	if _, err := store.Exec(r.Context(), s.DB, `UPDATE users SET commit_email_mode = ?, commit_email_custom = ? WHERE id = ?`, body.Mode, custom, p.User.ID); err != nil {
		api.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
