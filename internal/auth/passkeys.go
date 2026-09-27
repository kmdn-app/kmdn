package auth

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/audit"
	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

// Passkey is a registered credential (never its key material).
type Passkey struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	// Synced: a multi-device credential (backed up by the platform).
	Synced bool `json:"synced"`
}

// ceremonyTTL bounds how long a registration or sign-in may take.
const ceremonyTTL = 5 * time.Minute

type ceremony struct {
	session webauthn.SessionData
	userID  string // registration only
	name    string
	expires time.Time
}

// ceremonies holds pending WebAuthn challenges (single node, in memory).
type ceremonies struct {
	mu sync.Mutex
	m  map[string]ceremony
}

func (c *ceremonies) put(v ceremony) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]ceremony{}
	}
	now := time.Now()
	for k, x := range c.m {
		if now.After(x.expires) {
			delete(c.m, k)
		}
	}
	id := Token(18)
	v.expires = now.Add(ceremonyTTL)
	c.m[id] = v
	return id
}

// take returns and forgets a ceremony (challenges are single use).
func (c *ceremonies) take(id string) (ceremony, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[id]
	delete(c.m, id)
	if !ok || time.Now().After(v.expires) {
		return ceremony{}, false
	}
	return v, true
}

// relyingParty derives the WebAuthn relying party from the base URL: the
// RP ID is its host, the only allowed origin its scheme and host.
func (h *HTTP) relyingParty(ctx context.Context) (*webauthn.WebAuthn, error) {
	u, err := url.Parse(h.BaseURL)
	if err != nil || u.Hostname() == "" {
		return nil, errors.New("auth: passkeys need server.base_url")
	}
	return webauthn.New(&webauthn.Config{
		RPID:          u.Hostname(),
		RPDisplayName: InstanceName(ctx, h.Svc.DB),
		RPOrigins:     []string{u.Scheme + "://" + u.Host},
	})
}

// waUser adapts a user and their credentials to go-webauthn.
type waUser struct {
	u     users.User
	creds []webauthn.Credential
}

func (w waUser) WebAuthnID() []byte                         { return []byte(w.u.ID) }
func (w waUser) WebAuthnName() string                       { return w.u.Email }
func (w waUser) WebAuthnDisplayName() string                { return w.u.Name }
func (w waUser) WebAuthnCredentials() []webauthn.Credential { return w.creds }

func encodeID(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (h *HTTP) credentials(ctx context.Context, userID string) ([]webauthn.Credential, error) {
	rows, err := store.Query(ctx, h.Svc.DB, `SELECT credential FROM passkeys WHERE user_id = ? ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []webauthn.Credential
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var c webauthn.Credential
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListPasskeys returns a user's passkeys, oldest first.
func ListPasskeys(ctx context.Context, q store.Querier, userID string) ([]Passkey, error) {
	rows, err := store.Query(ctx, q, `SELECT id, name, credential, created_at, last_used_at FROM passkeys WHERE user_id = ? ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Passkey{}
	for rows.Next() {
		var p Passkey
		var raw string
		var created int64
		var used sql.NullInt64
		if err := rows.Scan(&p.ID, &p.Name, &raw, &created, &used); err != nil {
			return nil, err
		}
		var c webauthn.Credential
		_ = json.Unmarshal([]byte(raw), &c)
		p.Synced = c.Flags.BackupEligible
		p.CreatedAt = store.FromMillis(created)
		if used.Valid {
			t := store.FromMillis(used.Int64)
			p.LastUsedAt = &t
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (h *HTTP) passkeyRoutes(r chi.Router) {
	r.Post("/auth/passkey/options", h.passkeyOptions)
	r.Post("/auth/passkey/verify", h.passkeyVerify)
	r.Group(func(r chi.Router) {
		r.Use(Require)
		r.Get("/me/passkeys", h.listPasskeys)
		r.Post("/me/passkeys/register/options", h.registerOptions)
		r.Post("/me/passkeys/register/verify", h.registerVerify)
		r.Patch("/me/passkeys/{id}", h.renamePasskey)
		r.Delete("/me/passkeys/{id}", h.deletePasskey)
	})
}

func (h *HTTP) listPasskeys(w http.ResponseWriter, r *http.Request) {
	p, _ := FromContext(r.Context())
	list, err := ListPasskeys(r.Context(), h.Svc.DB, p.User.ID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": list})
}

func passkeyName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		s = "Passkey"
	}
	if len(s) > 60 {
		return "", api.Invalid("name", "Keep the name under 60 characters.")
	}
	return s, nil
}

// registerOptions starts adding a passkey: a discoverable credential, with
// the user's existing ones excluded.
func (h *HTTP) registerOptions(w http.ResponseWriter, r *http.Request) {
	p, _ := FromContext(r.Context())
	var body struct {
		Name string `json:"name"`
	}
	if err := api.Decode(r, &body); err != nil {
		api.Error(w, r, err)
		return
	}
	name, err := passkeyName(body.Name)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	wa, err := h.relyingParty(r.Context())
	if err != nil {
		api.Error(w, r, err)
		return
	}
	creds, err := h.credentials(r.Context(), p.User.ID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	u := waUser{p.User, creds}
	exclude := make([]protocol.CredentialDescriptor, len(creds))
	for i, c := range creds {
		exclude[i] = c.Descriptor()
	}
	opts, session, err := wa.BeginRegistration(u,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithExclusions(exclude),
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{ResidentKey: protocol.ResidentKeyRequirementRequired, RequireResidentKey: protocol.ResidentKeyRequired(), UserVerification: protocol.VerificationPreferred}))
	if err != nil {
		api.Error(w, r, err)
		return
	}
	id := h.passkeys.put(ceremony{session: *session, userID: p.User.ID, name: name})
	api.JSON(w, http.StatusOK, map[string]any{"ceremony": id, "options": opts})
}

// finish hands go-webauthn the credential from the JSON envelope
// {"ceremony": "...", "credential": {...}}.
func finishRequest(r *http.Request) (string, *http.Request, error) {
	var body struct {
		Ceremony   string          `json:"ceremony"`
		Credential json.RawMessage `json:"credential"`
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		return "", nil, err
	}
	if err := json.Unmarshal(b, &body); err != nil || body.Ceremony == "" || len(body.Credential) == 0 {
		return "", nil, api.Err(http.StatusBadRequest, "invalid_body", "Send the ceremony id and the credential.")
	}
	r2 := r.Clone(r.Context())
	r2.Body = io.NopCloser(bytes.NewReader(body.Credential))
	r2.ContentLength = int64(len(body.Credential))
	return body.Ceremony, r2, nil
}

var errCeremony = api.Err(http.StatusBadRequest, "passkey_expired", "That took too long or was already used: try again.")

func (h *HTTP) registerVerify(w http.ResponseWriter, r *http.Request) {
	p, _ := FromContext(r.Context())
	cid, r2, err := finishRequest(r)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	c, ok := h.passkeys.take(cid)
	if !ok || c.userID != p.User.ID {
		api.Error(w, r, errCeremony)
		return
	}
	wa, err := h.relyingParty(r.Context())
	if err != nil {
		api.Error(w, r, err)
		return
	}
	creds, err := h.credentials(r.Context(), p.User.ID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	cred, err := wa.FinishRegistration(waUser{p.User, creds}, c.session, r2)
	if err != nil {
		api.Error(w, r, api.Err(http.StatusBadRequest, "passkey_invalid", "The passkey couldn't be verified: "+protocolMessage(err)))
		return
	}
	raw, _ := json.Marshal(cred)
	pk := Passkey{ID: ids.New("pk"), Name: c.name, CreatedAt: time.Now().UTC(), Synced: cred.Flags.BackupEligible}
	if _, err := store.Exec(r.Context(), h.Svc.DB, `INSERT INTO passkeys (id, user_id, credential_id, credential, name, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		pk.ID, p.User.ID, encodeID(cred.ID), string(raw), pk.Name, store.Millis(pk.CreatedAt)); err != nil {
		if store.IsUniqueViolation(err) {
			api.Error(w, r, api.Err(http.StatusConflict, "passkey_exists", "This passkey is already registered."))
			return
		}
		api.Error(w, r, err)
		return
	}
	h.Svc.audit(r.Context(), audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID, IP: h.ip(r), Action: "auth.passkey_added", TargetType: "passkey", TargetID: pk.ID, Data: map[string]any{"name": pk.Name}})
	api.JSON(w, http.StatusCreated, pk)
}

// protocolMessage unwraps go-webauthn's details for the client.
func protocolMessage(err error) string {
	var pe *protocol.Error
	if errors.As(err, &pe) && pe.DevInfo != "" {
		return pe.Details + " (" + pe.DevInfo + ")"
	}
	return err.Error()
}

func (h *HTTP) renamePasskey(w http.ResponseWriter, r *http.Request) {
	p, _ := FromContext(r.Context())
	var body struct {
		Name string `json:"name"`
	}
	if err := api.Decode(r, &body); err != nil {
		api.Error(w, r, err)
		return
	}
	name, err := passkeyName(body.Name)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	res, err := store.Exec(r.Context(), h.Svc.DB, `UPDATE passkeys SET name = ? WHERE id = ? AND user_id = ?`, name, chi.URLParam(r, "id"), p.User.ID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		api.Error(w, r, api.ErrNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *HTTP) deletePasskey(w http.ResponseWriter, r *http.Request) {
	p, _ := FromContext(r.Context())
	id := chi.URLParam(r, "id")
	res, err := store.Exec(r.Context(), h.Svc.DB, `DELETE FROM passkeys WHERE id = ? AND user_id = ?`, id, p.User.ID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		api.Error(w, r, api.ErrNotFound)
		return
	}
	h.Svc.audit(r.Context(), audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID, IP: h.ip(r), Action: "auth.passkey_removed", TargetType: "passkey", TargetID: id})
	w.WriteHeader(http.StatusNoContent)
}

// passkeyOptions starts a discoverable sign-in: the browser offers the
// passkeys it has for this site, no email needed.
func (h *HTTP) passkeyOptions(w http.ResponseWriter, r *http.Request) {
	if !h.verifyByIP.Allow(h.ip(r)) {
		api.Error(w, r, api.ErrRateLimited)
		return
	}
	wa, err := h.relyingParty(r.Context())
	if err != nil {
		api.Error(w, r, err)
		return
	}
	opts, session, err := wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationPreferred))
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"ceremony": h.passkeys.put(ceremony{session: *session}), "options": opts})
}

func (h *HTTP) passkeyVerify(w http.ResponseWriter, r *http.Request) {
	if !h.verifyByIP.Allow(h.ip(r)) {
		api.Error(w, r, api.ErrRateLimited)
		return
	}
	cid, r2, err := finishRequest(r)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	c, ok := h.passkeys.take(cid)
	if !ok || c.userID != "" {
		api.Error(w, r, errCeremony)
		return
	}
	wa, err := h.relyingParty(r.Context())
	if err != nil {
		api.Error(w, r, err)
		return
	}
	var found users.User
	cred, err := wa.FinishDiscoverableLogin(func(rawID, userHandle []byte) (webauthn.User, error) {
		u, err := users.ByID(r.Context(), h.Svc.DB, string(userHandle))
		if err != nil {
			return nil, err
		}
		creds, err := h.credentials(r.Context(), u.ID)
		if err != nil {
			return nil, err
		}
		found = u
		return waUser{u, creds}, nil
	}, c.session, r2)
	if err != nil || found.ID == "" {
		h.Svc.audit(r.Context(), audit.Entry{ActorType: audit.ActorSystem, IP: h.ip(r), Action: "auth.sign_in_failed", Data: map[string]any{"method": "passkey"}})
		api.Error(w, r, api.Err(http.StatusUnauthorized, "passkey_invalid", "This passkey isn't registered here. Sign in with an email link, then add it in your profile."))
		return
	}
	if found.Status != users.Active {
		api.Error(w, r, api.Err(http.StatusForbidden, "deactivated", "This account is deactivated."))
		return
	}
	// A cloned authenticator shows up as a sign count going backwards.
	if cred.Authenticator.CloneWarning {
		h.Svc.audit(r.Context(), audit.Entry{ActorType: audit.ActorSystem, IP: h.ip(r), Action: "auth.passkey_clone_warning", TargetType: "user", TargetID: found.ID})
		api.Error(w, r, api.Err(http.StatusUnauthorized, "passkey_invalid", "This passkey looks copied. Sign in with an email link and replace it."))
		return
	}
	raw, _ := json.Marshal(cred)
	if _, err := store.Exec(r.Context(), h.Svc.DB, `UPDATE passkeys SET credential = ?, last_used_at = ? WHERE credential_id = ? AND user_id = ?`,
		string(raw), store.Millis(time.Now()), encodeID(cred.ID), found.ID); err != nil {
		api.Error(w, r, err)
		return
	}
	if err := h.SignIn(w, r, found, "passkey"); err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"user": found})
}
