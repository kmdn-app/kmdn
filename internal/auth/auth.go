// Package auth implements passwordless sign-in (magic links with a 6-digit
// fallback code), server-side sessions and CSRF protection. See
// docs/specs/09-auth-permissions.md.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"

	"github.com/kmdn-app/kmdn/internal/audit"
	"github.com/kmdn-app/kmdn/internal/mail"
	"github.com/kmdn-app/kmdn/internal/settings"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

const (
	LinkTTL         = 15 * time.Minute
	MaxCodeAttempts = 5
	touchEvery      = time.Hour
)

// Errors returned by verification. Handlers map them to one generic message so
// callers can't probe which emails exist.
var (
	ErrInvalidLink = errors.New("auth: invalid or expired sign-in link")
	ErrNoAccount   = errors.New("auth: no account for this email")
	ErrDeactivated = errors.New("auth: account deactivated")
)

// Service owns sign-in and sessions.
type Service struct {
	DB         *store.DB
	Mail       mail.Sender
	BaseURL    string
	SessionTTL time.Duration
	// AutoJoinDomains lets people with these email domains create an account
	// by signing in. Off (empty) by default.
	AutoJoinDomains []string
	Log             *slog.Logger
	now             func() time.Time
}

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Token returns n random bytes, base64url encoded.
func Token(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Hash returns the hex SHA-256 of s. Tokens are high-entropy, so a fast hash
// is appropriate.
func Hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func sixDigits() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%06d", n.Int64())
}

// InstanceName is shown in emails and the sign-in page.
func InstanceName(ctx context.Context, q store.Querier) string {
	var name string
	if err := settings.Get(ctx, q, "instance_name", &name); err != nil || name == "" {
		return "kmdn"
	}
	return name
}

func (s *Service) canAutoJoin(email string) bool {
	at := strings.LastIndexByte(email, '@')
	if at < 0 {
		return false
	}
	domain := email[at+1:]
	for _, d := range s.AutoJoinDomains {
		if strings.EqualFold(strings.TrimPrefix(d, "@"), domain) {
			return true
		}
	}
	return false
}

// RequestLink emails a sign-in link and code if email belongs to an active
// account (or an auto-join domain). It reports whether an email was sent, but
// handlers must not reveal that to the caller.
func (s *Service) RequestLink(ctx context.Context, email, ip string) (bool, error) {
	u, err := users.ByEmail(ctx, s.DB, email)
	switch {
	case err == nil && u.Status != users.Active:
		return false, nil
	case errors.Is(err, store.ErrNotFound) && !s.canAutoJoin(email):
		return false, nil
	case err != nil && !errors.Is(err, store.ErrNotFound):
		return false, err
	}
	token, code := Token(32), sixDigits()
	now := s.clock()
	if _, err := store.Exec(ctx, s.DB, `INSERT INTO magic_links (token_hash, email, code_hash, nonce_hash, created_at, expires_at, ip) VALUES (?, ?, ?, '', ?, ?, ?)`,
		Hash(token), email, Hash(email+":"+code), store.Millis(now), store.Millis(now.Add(LinkTTL)), ip); err != nil {
		return false, err
	}
	link := strings.TrimRight(s.BaseURL, "/") + "/auth/verify?token=" + token
	msg := mail.SignIn(InstanceName(ctx, s.DB), email, link, code, int(LinkTTL/time.Minute))
	if err := s.Mail.Send(ctx, msg); err != nil {
		return false, fmt.Errorf("sending sign-in email: %w", err)
	}
	return true, nil
}

// VerifyToken consumes a link token and returns the signed-in user.
func (s *Service) VerifyToken(ctx context.Context, token string) (users.User, error) {
	var u users.User
	err := s.DB.InTx(ctx, func(tx *store.Tx) error {
		var email string
		var exp int64
		err := store.QueryRow(ctx, tx, `SELECT email, expires_at FROM magic_links WHERE token_hash = ? AND used_at IS NULL`, Hash(token)).Scan(&email, &exp)
		if err != nil || store.FromMillis(exp).Before(s.clock()) {
			return ErrInvalidLink
		}
		if _, err := store.Exec(ctx, tx, `UPDATE magic_links SET used_at = ? WHERE token_hash = ?`, store.Millis(s.clock()), Hash(token)); err != nil {
			return err
		}
		u, err = s.userForSignIn(ctx, tx, email)
		return err
	})
	return u, err
}

// VerifyCode checks the 6-digit code against the pending links of email
// (usually one; several if the person asked again). A wrong code counts as an
// attempt on every pending link, so asking for new links doesn't reset the limit.
func (s *Service) VerifyCode(ctx context.Context, email, code string) (users.User, error) {
	var u users.User
	code = strings.TrimSpace(strings.ReplaceAll(code, " ", ""))
	want := Hash(email + ":" + code)
	var codeOK bool
	err := s.DB.InTx(ctx, func(tx *store.Tx) error {
		rows, err := store.Query(ctx, tx, `SELECT token_hash, code_hash FROM magic_links
			WHERE email = ? AND used_at IS NULL AND expires_at > ? AND attempts < ?`, email, store.Millis(s.clock()), MaxCodeAttempts)
		if err != nil {
			return err
		}
		var match string
		var pending []string
		for rows.Next() {
			var tokenHash, codeHash string
			if err := rows.Scan(&tokenHash, &codeHash); err != nil {
				rows.Close()
				return err
			}
			pending = append(pending, tokenHash)
			if subtle.ConstantTimeCompare([]byte(want), []byte(codeHash)) == 1 {
				match = tokenHash
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(pending) == 0 {
			return ErrInvalidLink
		}
		if match == "" {
			_, err := store.Exec(ctx, tx, `UPDATE magic_links SET attempts = attempts + 1 WHERE email = ? AND used_at IS NULL`, email)
			return err
		}
		codeOK = true
		if _, err := store.Exec(ctx, tx, `UPDATE magic_links SET used_at = ? WHERE token_hash = ?`, store.Millis(s.clock()), match); err != nil {
			return err
		}
		u, err = s.userForSignIn(ctx, tx, email)
		return err
	})
	if err == nil && !codeOK {
		return u, ErrInvalidLink
	}
	return u, err
}

func (s *Service) userForSignIn(ctx context.Context, q store.Querier, email string) (users.User, error) {
	u, err := users.ByEmail(ctx, q, email)
	if errors.Is(err, store.ErrNotFound) {
		if !s.canAutoJoin(email) {
			return u, ErrNoAccount
		}
		name := email[:strings.IndexByte(email, '@')]
		return users.Create(ctx, q, email, name, false)
	}
	if err != nil {
		return u, err
	}
	if u.Status != users.Active {
		return u, ErrDeactivated
	}
	return u, nil
}

// Session is a signed-in browser.
type Session struct {
	ID         string    `json:"id"` // hash of the cookie token, safe to show to its owner
	UserID     string    `json:"-"`
	CSRF       string    `json:"-"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	IP         string    `json:"ip"`
	UserAgent  string    `json:"user_agent"`
	Current    bool      `json:"current"`
}

func (s *Service) ttl() time.Duration {
	if s.SessionTTL > 0 {
		return s.SessionTTL
	}
	return 30 * 24 * time.Hour
}

// CreateSession starts a session and returns the raw cookie token.
func (s *Service) CreateSession(ctx context.Context, q store.Querier, userID, ip, ua string) (token string, sess Session, err error) {
	token = Token(32)
	now := s.clock()
	if len(ua) > 300 {
		ua = ua[:300]
	}
	sess = Session{ID: Hash(token), UserID: userID, CSRF: Token(24), CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(s.ttl()), IP: ip, UserAgent: ua}
	_, err = store.Exec(ctx, q, `INSERT INTO sessions (id_hash, user_id, csrf_token, created_at, last_seen_at, expires_at, ip, user_agent) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, userID, sess.CSRF, store.Millis(now), store.Millis(now), store.Millis(sess.ExpiresAt), ip, ua)
	return token, sess, err
}

// Lookup resolves a cookie token. Sessions roll forward on use.
func (s *Service) Lookup(ctx context.Context, token string) (Session, users.User, error) {
	var sess Session
	var created, seen, exp int64
	err := store.QueryRow(ctx, s.DB, `SELECT id_hash, user_id, csrf_token, created_at, last_seen_at, expires_at, ip, user_agent FROM sessions WHERE id_hash = ?`, Hash(token)).
		Scan(&sess.ID, &sess.UserID, &sess.CSRF, &created, &seen, &exp, &sess.IP, &sess.UserAgent)
	if err != nil {
		return sess, users.User{}, store.NotFound(err)
	}
	sess.CreatedAt, sess.LastSeenAt, sess.ExpiresAt = store.FromMillis(created), store.FromMillis(seen), store.FromMillis(exp)
	now := s.clock()
	if sess.ExpiresAt.Before(now) {
		_ = s.Revoke(ctx, sess.ID)
		return sess, users.User{}, store.ErrNotFound
	}
	u, err := users.ByID(ctx, s.DB, sess.UserID)
	if err != nil {
		return sess, u, err
	}
	if u.Status != users.Active {
		return sess, u, ErrDeactivated
	}
	if now.Sub(sess.LastSeenAt) > touchEvery {
		sess.LastSeenAt, sess.ExpiresAt = now, now.Add(s.ttl())
		if _, err := store.Exec(ctx, s.DB, `UPDATE sessions SET last_seen_at = ?, expires_at = ? WHERE id_hash = ?`, store.Millis(now), store.Millis(sess.ExpiresAt), sess.ID); err != nil {
			return sess, u, err
		}
		_ = users.Touch(ctx, s.DB, u.ID, now)
	}
	return sess, u, nil
}

// Revoke ends one session.
func (s *Service) Revoke(ctx context.Context, id string) error {
	_, err := store.Exec(ctx, s.DB, `DELETE FROM sessions WHERE id_hash = ?`, id)
	return err
}

// RevokeUser ends every session of a user (except keep, if set).
func (s *Service) RevokeUser(ctx context.Context, userID, keep string) error {
	_, err := store.Exec(ctx, s.DB, `DELETE FROM sessions WHERE user_id = ? AND id_hash <> ?`, userID, keep)
	return err
}

// Sessions lists a user's active sessions, newest first.
func (s *Service) Sessions(ctx context.Context, userID, current string) ([]Session, error) {
	rows, err := store.Query(ctx, s.DB, `SELECT id_hash, created_at, last_seen_at, expires_at, ip, user_agent FROM sessions WHERE user_id = ? AND expires_at > ? ORDER BY last_seen_at DESC`,
		userID, store.Millis(s.clock()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var ss Session
		var c, l, e int64
		if err := rows.Scan(&ss.ID, &c, &l, &e, &ss.IP, &ss.UserAgent); err != nil {
			return nil, err
		}
		ss.CreatedAt, ss.LastSeenAt, ss.ExpiresAt = store.FromMillis(c), store.FromMillis(l), store.FromMillis(e)
		ss.Current = ss.ID == current
		out = append(out, ss)
	}
	return out, rows.Err()
}

// PurgeExpired deletes expired sessions and old magic links (run periodically).
func (s *Service) PurgeExpired(ctx context.Context) error {
	now := store.Millis(s.clock())
	if _, err := store.Exec(ctx, s.DB, `DELETE FROM sessions WHERE expires_at < ?`, now); err != nil {
		return err
	}
	_, err := store.Exec(ctx, s.DB, `DELETE FROM magic_links WHERE expires_at < ?`, now-int64(24*time.Hour/time.Millisecond))
	return err
}

func (s *Service) audit(ctx context.Context, e audit.Entry) {
	if err := audit.Write(ctx, s.DB, e); err != nil && s.Log != nil {
		s.Log.Error("audit write failed", "action", e.Action, "error", err)
	}
}
