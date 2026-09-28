// Package mcp is kmdn's read-only Model Context Protocol server: agent keys
// scoped to repos, and tools and resources over published content
// (docs/specs/12-mcp.md).
package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/orgs"
	"github.com/kmdn-app/kmdn/internal/store"
)

// KeyPrefix starts every agent key.
const KeyPrefix = "kmdn_ak_"

// Key is an agent key (never with its secret, which is shown once).
type Key struct {
	ID          string     `json:"id"`
	OrgID       string     `json:"org_id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	AllRepos    bool       `json:"all_repos"`
	RepoIDs     []string   `json:"repo_ids"`
	CreatedBy   string     `json:"created_by,omitempty"`
	CreatorName string     `json:"created_by_name,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	LastUsedIP  string     `json:"last_used_ip,omitempty"`
	// Status: active | revoked | expired.
	Status string `json:"status"`
	// Calls per UTC day, oldest first (the last 14 days).
	Usage []int `json:"usage"`
}

// Sees reports whether the key covers a repo.
// Keys only see repos of their own org.
func (k Key) Sees(r repos.Repo) bool {
	if r.OrgID != k.OrgID {
		return false
	}
	if k.AllRepos {
		return true
	}
	for _, id := range k.RepoIDs {
		if id == r.ID {
			return true
		}
	}
	return false
}

func (k *Key) status(now time.Time) {
	switch {
	case k.RevokedAt != nil:
		k.Status = "revoked"
	case k.ExpiresAt != nil && !k.ExpiresAt.After(now):
		k.Status = "expired"
	default:
		k.Status = "active"
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func hashSecret(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// NewKey is what creating a key needs.
type NewKey struct {
	OrgID       string
	Name        string
	Description string
	AllRepos    bool
	RepoIDs     []string
	// ExpiresIn: 0 never expires.
	ExpiresIn time.Duration
	CreatedBy string
}

// CreateKey stores a key and returns it with its full token (shown once).
func CreateKey(ctx context.Context, db *store.DB, in NewKey) (Key, string, error) {
	id := randomHex(8)
	secret := auth.Token(32)
	now := time.Now()
	k := Key{ID: id, OrgID: in.OrgID, Name: in.Name, Description: in.Description, AllRepos: in.AllRepos, RepoIDs: in.RepoIDs, CreatedBy: in.CreatedBy, CreatedAt: now.UTC()}
	var exp any
	if in.ExpiresIn > 0 {
		t := now.Add(in.ExpiresIn).UTC()
		k.ExpiresAt = &t
		exp = store.Millis(t)
	}
	if k.AllRepos {
		k.RepoIDs = []string{}
	}
	err := db.InTx(ctx, func(tx *store.Tx) error {
		if _, err := store.Exec(ctx, tx, `INSERT INTO agent_keys (id, org_id, name, description, all_repos, secret_hash, created_by, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, in.OrgID, in.Name, in.Description, in.AllRepos, hashSecret(secret), in.CreatedBy, store.Millis(now), exp); err != nil {
			return err
		}
		for _, r := range k.RepoIDs {
			if _, err := store.Exec(ctx, tx, `INSERT INTO agent_key_repos (key_id, repo_id) VALUES (?, ?) ON CONFLICT DO NOTHING`, id, r); err != nil {
				return err
			}
		}
		return nil
	})
	k.status(now)
	return k, KeyPrefix + id + "_" + secret, err
}

const keyCols = `k.id, k.name, k.description, k.all_repos, COALESCE(k.created_by, ''), COALESCE(u.name, ''), k.created_at, k.expires_at, k.revoked_at, k.last_used_at, k.last_used_ip, k.org_id`

func scanKey(sc interface{ Scan(...any) error }) (Key, error) {
	var k Key
	var created int64
	var exp, rev, used sql.NullInt64
	if err := sc.Scan(&k.ID, &k.Name, &k.Description, &k.AllRepos, &k.CreatedBy, &k.CreatorName, &created, &exp, &rev, &used, &k.LastUsedIP, &k.OrgID); err != nil {
		return k, err
	}
	k.CreatedAt = time.UnixMilli(created).UTC()
	at := func(v sql.NullInt64) *time.Time {
		if !v.Valid {
			return nil
		}
		t := time.UnixMilli(v.Int64).UTC()
		return &t
	}
	k.ExpiresAt, k.RevokedAt, k.LastUsedAt = at(exp), at(rev), at(used)
	k.RepoIDs = []string{}
	k.status(time.Now())
	return k, nil
}

func loadRepos(ctx context.Context, q store.Querier, k *Key) error {
	if k.AllRepos {
		return nil
	}
	rows, err := store.Query(ctx, q, `SELECT repo_id FROM agent_key_repos WHERE key_id = ? ORDER BY repo_id`, k.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		k.RepoIDs = append(k.RepoIDs, id)
	}
	return rows.Err()
}

// GetKey loads a key by id.
func GetKey(ctx context.Context, db *store.DB, id string) (Key, error) {
	k, err := scanKey(store.QueryRow(ctx, db, `SELECT `+keyCols+` FROM agent_keys k LEFT JOIN users u ON u.id = k.created_by WHERE k.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return k, store.ErrNotFound
	}
	if err != nil {
		return k, err
	}
	return k, loadRepos(ctx, db, &k)
}

// ListKeys returns an org's keys, newest first, with 14 days of usage.
func ListKeys(ctx context.Context, db *store.DB, orgID string) ([]Key, error) {
	rows, err := store.Query(ctx, db, `SELECT `+keyCols+` FROM agent_keys k LEFT JOIN users u ON u.id = k.created_by WHERE k.org_id = ? ORDER BY k.created_at DESC`, orgID)
	if err != nil {
		return nil, err
	}
	out := []Key{}
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	days := make([]string, 14)
	idx := map[string]int{}
	today := time.Now().UTC()
	for i := range days {
		days[i] = today.AddDate(0, 0, i-13).Format(time.DateOnly)
		idx[days[i]] = i
	}
	for i := range out {
		if err := loadRepos(ctx, db, &out[i]); err != nil {
			return nil, err
		}
		out[i].Usage = make([]int, 14)
		urows, err := store.Query(ctx, db, `SELECT day, calls FROM agent_key_usage WHERE key_id = ? AND day >= ?`, out[i].ID, days[0])
		if err != nil {
			return nil, err
		}
		for urows.Next() {
			var d string
			var n int
			if err := urows.Scan(&d, &n); err != nil {
				urows.Close()
				return nil, err
			}
			if j, ok := idx[d]; ok {
				out[i].Usage[j] = n
			}
		}
		urows.Close()
	}
	return out, nil
}

// RevokeKey ends a key now.
func RevokeKey(ctx context.Context, db *store.DB, id string) error {
	res, err := store.Exec(ctx, db, `UPDATE agent_keys SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, store.Millis(time.Now()), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// authError is an authentication failure, worded for the agent's user.
type authError string

func (e authError) Error() string { return string(e) }

// Authentication failures.
const (
	ErrBadKey  authError = "This isn't a valid kmdn agent key."
	ErrRevoked authError = "This agent key was revoked. Ask an admin for a new one."
)

// ErrExpired is an expired key.
type ErrExpired struct{ At time.Time }

func (e ErrExpired) Error() string {
	return "This agent key expired on " + e.At.Format("2 January 2006") + ". Ask an admin for a new one."
}

// Authenticate checks a bearer token: lookup by id, constant-time compare.
func Authenticate(ctx context.Context, db *store.DB, token string) (Key, error) {
	rest, ok := strings.CutPrefix(token, KeyPrefix)
	if !ok {
		return Key{}, ErrBadKey
	}
	id, secret, ok := strings.Cut(rest, "_")
	if !ok || len(id) != 16 || secret == "" {
		return Key{}, ErrBadKey
	}
	var hash string
	err := store.QueryRow(ctx, db, `SELECT secret_hash FROM agent_keys WHERE id = ?`, id).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return Key{}, ErrBadKey
	}
	if err != nil {
		return Key{}, err
	}
	if subtle.ConstantTimeCompare([]byte(hash), []byte(hashSecret(secret))) != 1 {
		return Key{}, ErrBadKey
	}
	k, err := GetKey(ctx, db, id)
	if err != nil {
		return k, err
	}
	switch k.Status {
	case "revoked":
		return k, ErrRevoked
	case "expired":
		return k, ErrExpired{*k.ExpiresAt}
	}
	// An org being deleted is unreachable at once, by its keys too.
	var status string
	if err := store.QueryRow(ctx, db, `SELECT status FROM orgs WHERE id = ?`, k.OrgID).Scan(&status); err != nil || status == orgs.Deleting {
		return k, ErrBadKey
	}
	return k, nil
}

// touch records a key's last use (callers throttle it).
func touch(ctx context.Context, db *store.DB, id, ip string) error {
	_, err := store.Exec(ctx, db, `UPDATE agent_keys SET last_used_at = ?, last_used_ip = ? WHERE id = ?`, store.Millis(time.Now()), ip, id)
	return err
}

// count adds a call to today's usage.
func count(ctx context.Context, db *store.DB, id string) error {
	_, err := store.Exec(ctx, db, `INSERT INTO agent_key_usage (key_id, day, calls) VALUES (?, ?, 1) ON CONFLICT (key_id, day) DO UPDATE SET calls = agent_key_usage.calls + 1`,
		id, time.Now().UTC().Format(time.DateOnly))
	return err
}
