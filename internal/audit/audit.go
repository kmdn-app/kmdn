// Package audit appends security and content events to the audit log.
// See docs/specs/13-operations.md#audit-log.
package audit

import (
	"context"
	"encoding/json"
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
	ActorType  string
	ActorID    string
	IP         string
	Action     string // e.g. "auth.sign_in", "user.invited"
	TargetType string
	TargetID   string
	RepoID     string
	Data       map[string]any
}

// Record is a stored entry.
type Record struct {
	Entry
	ID string
	At time.Time
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
	_, err := store.Exec(ctx, q, `INSERT INTO audit_log (id, at, actor_type, actor_id, ip, action, target_type, target_id, repo_id, data)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ids.New(ids.Audit), store.Millis(time.Now()), e.ActorType, nullable(e.ActorID), e.IP, e.Action,
		nullable(e.TargetType), nullable(e.TargetID), nullable(e.RepoID), data)
	return err
}

// Recent returns the latest entries, newest first (used by tests and the admin console).
func Recent(ctx context.Context, q store.Querier, limit int) ([]Record, error) {
	rows, err := store.Query(ctx, q, `SELECT id, at, actor_type, COALESCE(actor_id, ''), ip, action, COALESCE(target_type, ''), COALESCE(target_id, ''), COALESCE(repo_id, ''), data
		FROM audit_log ORDER BY at DESC, id DESC LIMIT ?`, limit)
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
	return out, rows.Err()
}
