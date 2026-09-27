// Package audit appends security and content events to the audit log.
// See docs/specs/13-operations.md#audit-log.
package audit

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/store"
)

// Actor types.
const (
	ActorUser      = "user"
	ActorSystem    = "system"
	ActorAgentKey  = "agent_key"
	ActorAssistant = "assistant"
)

// Entry is one audit event. Never put secret values in Data.
type Entry struct {
	ActorType  string `json:"actor_type"`
	ActorID    string `json:"actor_id,omitempty"`
	IP         string `json:"ip,omitempty"`
	Action     string `json:"action"` // e.g. "auth.sign_in", "user.invited"
	TargetType string `json:"target_type,omitempty"`
	TargetID   string `json:"target_id,omitempty"`
	RepoID     string `json:"repo_id,omitempty"`
	// OrgID is the org the event belongs to; when empty, the repo's org, or
	// none for instance events.
	OrgID string         `json:"org_id,omitempty"`
	Data  map[string]any `json:"data,omitempty"`
}

// Record is a stored entry.
type Record struct {
	Entry
	ID string    `json:"id"`
	At time.Time `json:"at"`
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Write appends e.
func Write(ctx context.Context, q store.Querier, e Entry) error {
	data := "{}"
	if len(e.Data) > 0 {
		b, err := json.Marshal(e.Data)
		if err != nil {
			return err
		}
		data = string(b)
	}
	_, err := store.Exec(ctx, q, `INSERT INTO audit_log (id, at, actor_type, actor_id, ip, action, target_type, target_id, repo_id, data, org_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, COALESCE(?, (SELECT org_id FROM repos WHERE id = ?)))`,
		ids.New(ids.Audit), store.Millis(time.Now()), e.ActorType, nullable(e.ActorID), e.IP, e.Action,
		nullable(e.TargetType), nullable(e.TargetID), nullable(e.RepoID), data, nullable(e.OrgID), e.RepoID)
	return err
}

// Recent returns the latest entries, newest first (used by tests and the admin console).
func Recent(ctx context.Context, q store.Querier, limit int) ([]Record, error) {
	return list(ctx, q, `SELECT id, at, actor_type, COALESCE(actor_id, ''), ip, action, COALESCE(target_type, ''), COALESCE(target_id, ''), COALESCE(repo_id, ''), data
		FROM audit_log ORDER BY at DESC, id DESC LIMIT ?`, limit)
}

// ByActor returns an actor's most recent entries (an agent key's calls).
func ByActor(ctx context.Context, q store.Querier, actorType, actorID string, limit int) ([]Record, error) {
	return list(ctx, q, `SELECT id, at, actor_type, COALESCE(actor_id, ''), ip, action, COALESCE(target_type, ''), COALESCE(target_id, ''), COALESCE(repo_id, ''), data
		FROM audit_log WHERE actor_type = ? AND actor_id = ? ORDER BY at DESC, id DESC LIMIT ?`, actorType, actorID, limit)
}

func list(ctx context.Context, q store.Querier, query string, args ...any) ([]Record, error) {
	rows, err := store.Query(ctx, q, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var r Record
		var at int64
		var data string
		if err := rows.Scan(&r.ID, &at, &r.ActorType, &r.ActorID, &r.IP, &r.Action, &r.TargetType, &r.TargetID, &r.RepoID, &data); err != nil {
			return nil, err
		}
		r.At = store.FromMillis(at)
		_ = json.Unmarshal([]byte(data), &r.Data)
		out = append(out, r)
	}
	if out == nil {
		out = []Record{}
	}
	return out, rows.Err()
}

// Filter narrows a query. Zero fields match everything.
type Filter struct {
	ActorType string
	ActorID   string
	// Action matches exactly, or a family with a trailing "." ("revision.").
	Action string
	RepoID string
	// OrgID keeps one org's entries (empty: every entry, for instance admins).
	OrgID string
	From  time.Time // inclusive
	To    time.Time // exclusive
	// Before continues a listing after this entry (newest first).
	Before *Record
	Limit  int
}

// Named is a record with display names for its actor and repo.
type Named struct {
	Record
	ActorName string `json:"actor_name,omitempty"`
	RepoName  string `json:"repo_name,omitempty"`
}

// Query returns matching entries, newest first, with names resolved.
func Query(ctx context.Context, q store.Querier, f Filter) ([]Named, error) {
	where := []string{"1 = 1"}
	var args []any
	if f.ActorType != "" {
		where, args = append(where, "a.actor_type = ?"), append(args, f.ActorType)
	}
	if f.ActorID != "" {
		where, args = append(where, "a.actor_id = ?"), append(args, f.ActorID)
	}
	if f.Action != "" {
		if strings.HasSuffix(f.Action, ".") {
			where, args = append(where, "substr(a.action, 1, ?) = ?"), append(args, len(f.Action), f.Action)
		} else {
			where, args = append(where, "a.action = ?"), append(args, f.Action)
		}
	}
	if f.RepoID != "" {
		where, args = append(where, "a.repo_id = ?"), append(args, f.RepoID)
	}
	if f.OrgID != "" {
		where, args = append(where, "a.org_id = ?"), append(args, f.OrgID)
	}
	if !f.From.IsZero() {
		where, args = append(where, "a.at >= ?"), append(args, store.Millis(f.From))
	}
	if !f.To.IsZero() {
		where, args = append(where, "a.at < ?"), append(args, store.Millis(f.To))
	}
	if f.Before != nil {
		at := store.Millis(f.Before.At)
		where, args = append(where, "(a.at < ? OR (a.at = ? AND a.id < ?))"), append(args, at, at, f.Before.ID)
	}
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	args = append(args, limit)
	rows, err := store.Query(ctx, q, `SELECT a.id, a.at, a.actor_type, COALESCE(a.actor_id, ''), a.ip, a.action, COALESCE(a.target_type, ''), COALESCE(a.target_id, ''), COALESCE(a.repo_id, ''), a.data,
		COALESCE(u.name, k.name, ''), COALESCE(r.owner || '/' || r.name, '')
		FROM audit_log a
		LEFT JOIN users u ON a.actor_type IN ('user', 'assistant') AND u.id = a.actor_id
		LEFT JOIN agent_keys k ON a.actor_type = 'agent_key' AND k.id = a.actor_id
		LEFT JOIN repos r ON r.id = a.repo_id
		WHERE `+strings.Join(where, " AND ")+` ORDER BY a.at DESC, a.id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Named{}
	for rows.Next() {
		var n Named
		var at int64
		var data string
		if err := rows.Scan(&n.ID, &at, &n.ActorType, &n.ActorID, &n.IP, &n.Action, &n.TargetType, &n.TargetID, &n.RepoID, &data, &n.ActorName, &n.RepoName); err != nil {
			return nil, err
		}
		n.At = store.FromMillis(at)
		_ = json.Unmarshal([]byte(data), &n.Data)
		out = append(out, n)
	}
	return out, rows.Err()
}

// Actions lists the distinct actions recorded (the filter's choices).
// An empty orgID lists the actions of every org and of the instance.
func Actions(ctx context.Context, q store.Querier, orgID string) ([]string, error) {
	query, args := `SELECT DISTINCT action FROM audit_log ORDER BY action`, []any(nil)
	if orgID != "" {
		query, args = `SELECT DISTINCT action FROM audit_log WHERE org_id = ? ORDER BY action`, []any{orgID}
	}
	rows, err := store.Query(ctx, q, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
