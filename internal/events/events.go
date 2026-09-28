// Package events tells an embedding program what happened in orgs: people
// joining or leaving, roles, repositories, AI use (for plans, seats and
// billing). Handlers run after the change committed and must not block.
package events

import (
	"context"
	"time"
)

// Types.
const (
	OrgCreated       = "org.created"
	OrgStatusChanged = "org.status_changed"
	MemberAdded      = "member.added"
	MemberChanged    = "member.changed" // role or status
	MemberRemoved    = "member.removed"
	RepoConnected    = "repo.connected"
	RepoRemoved      = "repo.removed"
	AIUsage          = "ai.usage" // Data: tokens, task, model
)

// Event is something that happened in an org.
type Event struct {
	Type   string         `json:"type"`
	OrgID  string         `json:"org_id"`
	UserID string         `json:"user_id,omitempty"` // the person it's about
	Data   map[string]any `json:"data,omitempty"`
	At     time.Time      `json:"at"`
}

// Sink receives events; nil drops them.
type Sink func(ctx context.Context, e Event)

// Emit sends e to the sink, if any.
func (s Sink) Emit(ctx context.Context, e Event) {
	if s == nil {
		return
	}
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	s(context.WithoutCancel(ctx), e)
}
