// Package revisions holds proposed changes to a repository: the revision
// record, its file manifest, members and event log. Content lives in Y.Docs
// (internal/collab); this package keeps the materialized markdown per file.
// See docs/specs/05-collaboration.md and docs/specs/07-review.md.
package revisions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/kmdn-app/kmdn/internal/audit"
	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/store"
)

// State is a revision's lifecycle state.
type State string

// Revision states (docs/specs/07-review.md#revision-lifecycle).
const (
	Editing    State = "editing"
	InReview   State = "in_review"
	Approved   State = "approved"
	Publishing State = "publishing"
	Published  State = "published"
	Closed     State = "closed"
)

// Open reports whether the revision is still being worked on.
func (s State) Open() bool { return s == Editing || s == InReview || s == Approved || s == Publishing }

// Member roles.
const (
	RoleOwner  = "owner"
	RoleEditor = "editor"
)

// File operations in the manifest.
const (
	OpModify = "modify"
	OpAdd    = "add"
	OpDelete = "delete"
	OpRename = "rename"
)

// Revision is a proposed change set.
type Revision struct {
	ID               string     `json:"id"`
	RepoID           string     `json:"repo_id"`
	Number           int        `json:"number"`
	Title            string     `json:"title"`
	Description      string     `json:"description"`
	State            State      `json:"state"`
	ChangesRequested bool       `json:"changes_requested"`
	HasConflicts     bool       `json:"has_conflicts"`
	ReviewRound      int        `json:"review_round"`
	BaseSHA          string     `json:"base_sha"`
	CreatedBy        string     `json:"created_by"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	SubmittedAt      *time.Time `json:"submitted_at,omitempty"`
	PublishedAt      *time.Time `json:"published_at,omitempty"`
	PublishedSHA     string     `json:"published_sha,omitempty"`
	ChangeRequestURL string     `json:"change_request_url,omitempty"`
	ClosedAt         *time.Time `json:"closed_at,omitempty"`
	// Branch is the revision's branch on the forge (kmdn/<number>-<slug>),
	// empty until kmdn pushed it; BranchSHA its tip as kmdn last pushed it.
	Branch    string `json:"branch,omitempty"`
	BranchSHA string `json:"branch_sha,omitempty"`
	// ChangeRequestDraft: the pull/merge request is a draft (while Editing).
	ChangeRequestDraft bool `json:"change_request_draft"`

	// BranchBaseSHA is the Published commit the branch last merged.
	BranchBaseSHA     string `json:"-"`
	ChangeRequestRef  string `json:"-"`
	ChangeRequestNode string `json:"-"`
}

// Member is someone editing the revision.
type Member struct {
	UserID  string    `json:"user_id"`
	Name    string    `json:"name"`
	Email   string    `json:"email"`
	Role    string    `json:"role"`
	AddedAt time.Time `json:"added_at"`
}

// File is a manifest entry.
type File struct {
	ID             string     `json:"id"`
	Path           string     `json:"path"`
	Op             string     `json:"op"`
	FromPath       string     `json:"from_path,omitempty"`
	ContentHash    string     `json:"content_hash,omitempty"`
	MaterializedAt *time.Time `json:"materialized_at,omitempty"`
	HasConflicts   bool       `json:"has_conflicts"`
	// Conflict is a page-level conflict (deleted_upstream: Published deleted
	// a page the revision edits); empty for conflict blocks in the page.
	Conflict  string    `json:"conflict,omitempty"`
	Additions int       `json:"additions"`
	Deletions int       `json:"deletions"`
	UpdatedAt time.Time `json:"updated_at"`

	BaseMD    string `json:"-"`
	ContentMD string `json:"-"`
	YDocID    string `json:"-"`
}

// Event is an entry in the revision's activity log.
type Event struct {
	ID        string          `json:"id"`
	ActorType string          `json:"actor_type"`
	ActorID   string          `json:"actor_id,omitempty"`
	ActorName string          `json:"actor_name,omitempty"`
	Kind      string          `json:"kind"`
	Data      json.RawMessage `json:"data"`
	CreatedAt time.Time       `json:"created_at"`
}

const revCols = `id, repo_id, number, title, description, state, changes_requested, has_conflicts, review_round, base_sha,
	COALESCE(created_by, ''), created_at, updated_at, submitted_at, published_at, published_sha, change_request_url, closed_at,
	branch, branch_sha, branch_base_sha, change_request_ref, change_request_node, change_request_draft`

func scanRevision(row interface{ Scan(...any) error }) (Revision, error) {
	var r Revision
	var created, updated int64
	var submitted, published, closed sql.NullInt64
	err := row.Scan(&r.ID, &r.RepoID, &r.Number, &r.Title, &r.Description, &r.State, &r.ChangesRequested, &r.HasConflicts,
		&r.ReviewRound, &r.BaseSHA, &r.CreatedBy, &created, &updated, &submitted, &published, &r.PublishedSHA, &r.ChangeRequestURL, &closed,
		&r.Branch, &r.BranchSHA, &r.BranchBaseSHA, &r.ChangeRequestRef, &r.ChangeRequestNode, &r.ChangeRequestDraft)
	if err != nil {
		return r, store.NotFound(err)
	}
	r.CreatedAt, r.UpdatedAt = store.FromMillis(created), store.FromMillis(updated)
	r.SubmittedAt, r.PublishedAt, r.ClosedAt = store.NullMillis(submitted), store.NullMillis(published), store.NullMillis(closed)
	return r, nil
}

// Get loads a revision by id.
func Get(ctx context.Context, q store.Querier, id string) (Revision, error) {
	return scanRevision(store.QueryRow(ctx, q, `SELECT `+revCols+` FROM revisions WHERE id = ?`, id))
}

// ByNumber loads a revision by its per-repo number.
func ByNumber(ctx context.Context, q store.Querier, repoID string, n int) (Revision, error) {
	return scanRevision(store.QueryRow(ctx, q, `SELECT `+revCols+` FROM revisions WHERE repo_id = ? AND number = ?`, repoID, n))
}

// Filter narrows List.
type Filter struct {
	States []State
	Member string // only revisions this user edits
	// Reviewer: only revisions in review that ask this user to review and
	// that they haven't approved in the current round.
	Reviewer string
	Limit    int
}

// List returns a repo's revisions, most recently updated first.
func List(ctx context.Context, q store.Querier, repoID string, f Filter) ([]Revision, error) {
	where, args := []string{"repo_id = ?"}, []any{repoID}
	if len(f.States) > 0 {
		ph := make([]string, len(f.States))
		for i, s := range f.States {
			ph[i], args = "?", append(args, string(s))
		}
		where = append(where, "state IN ("+strings.Join(ph, ", ")+")")
	}
	if f.Member != "" {
		where, args = append(where, "id IN (SELECT revision_id FROM revision_members WHERE user_id = ?)"), append(args, f.Member)
	}
	if f.Reviewer != "" {
		where = append(where, "state = ?",
			"id IN (SELECT revision_id FROM revision_reviewers WHERE user_id = ? AND removed_at IS NULL)",
			"NOT EXISTS (SELECT 1 FROM approvals a WHERE a.revision_id = revisions.id AND a.user_id = ? AND a.review_round = revisions.review_round AND a.dismissed_at IS NULL)")
		args = append(args, string(InReview), f.Reviewer, f.Reviewer)
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := store.Query(ctx, q, `SELECT `+revCols+` FROM revisions WHERE `+strings.Join(where, " AND ")+` ORDER BY updated_at DESC, number DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Revision{}
	for rows.Next() {
		r, err := scanRevision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Members lists a revision's editors, owner first.
func Members(ctx context.Context, q store.Querier, revisionID string) ([]Member, error) {
	rows, err := store.Query(ctx, q, `SELECT m.user_id, u.name, u.email, m.role, m.added_at FROM revision_members m
		JOIN users u ON u.id = m.user_id WHERE m.revision_id = ? ORDER BY CASE m.role WHEN 'owner' THEN 0 ELSE 1 END, m.added_at`, revisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		var m Member
		var added int64
		if err := rows.Scan(&m.UserID, &m.Name, &m.Email, &m.Role, &added); err != nil {
			return nil, err
		}
		m.AddedAt = store.FromMillis(added)
		out = append(out, m)
	}
	return out, rows.Err()
}

// IsMember reports whether the user edits the revision.
func IsMember(ctx context.Context, q store.Querier, revisionID, userID string) (bool, error) {
	var n int
	err := store.QueryRow(ctx, q, `SELECT COUNT(*) FROM revision_members WHERE revision_id = ? AND user_id = ?`, revisionID, userID).Scan(&n)
	return n > 0, err
}

const fileCols = `id, path, op, from_path, content_hash, materialized_at, has_conflicts, conflict, additions, deletions, updated_at, base_md, content_md, COALESCE(ydoc_id, '')`

func scanFile(row interface{ Scan(...any) error }) (File, error) {
	var f File
	var mat sql.NullInt64
	var updated int64
	err := row.Scan(&f.ID, &f.Path, &f.Op, &f.FromPath, &f.ContentHash, &mat, &f.HasConflicts, &f.Conflict, &f.Additions, &f.Deletions, &updated, &f.BaseMD, &f.ContentMD, &f.YDocID)
	if err != nil {
		return f, store.NotFound(err)
	}
	f.MaterializedAt, f.UpdatedAt = store.NullMillis(mat), store.FromMillis(updated)
	return f, nil
}

// Files returns the manifest ordered by path.
func Files(ctx context.Context, q store.Querier, revisionID string) ([]File, error) {
	rows, err := store.Query(ctx, q, `SELECT `+fileCols+` FROM revision_files WHERE revision_id = ? ORDER BY path`, revisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []File{}
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// FileAt returns the manifest entry at path.
func FileAt(ctx context.Context, q store.Querier, revisionID, path string) (File, error) {
	return scanFile(store.QueryRow(ctx, q, `SELECT `+fileCols+` FROM revision_files WHERE revision_id = ? AND path = ?`, revisionID, path))
}

// fileFrom returns the manifest entry renamed from path, if any.
func fileFrom(ctx context.Context, q store.Querier, revisionID, path string) (File, error) {
	return scanFile(store.QueryRow(ctx, q, `SELECT `+fileCols+` FROM revision_files WHERE revision_id = ? AND from_path = ?`, revisionID, path))
}

// Actor types for events.
const (
	ActorUser      = "user"
	ActorAssistant = "assistant"
	ActorSystem    = "system"
)

// Record appends an event to the revision's log and bumps updated_at.
func Record(ctx context.Context, q store.Querier, revisionID, actorType, actorID, kind string, data any) error {
	b := []byte("{}")
	if data != nil {
		var err error
		if b, err = json.Marshal(data); err != nil {
			return err
		}
	}
	now := store.Millis(time.Now())
	if _, err := store.Exec(ctx, q, `INSERT INTO revision_events (id, revision_id, actor_type, actor_id, kind, data, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ids.New("rve"), revisionID, actorType, actorID, kind, string(b), now); err != nil {
		return err
	}
	if _, err := store.Exec(ctx, q, `UPDATE revisions SET updated_at = ? WHERE id = ?`, now, revisionID); err != nil {
		return err
	}
	return auditEvent(ctx, q, revisionID, actorType, actorID, kind, b)
}

// audited maps revision events to audit log actions (docs/specs/13-operations.md#audit-log).
// Publishing is audited by the publisher, with the commit.
var audited = map[string]string{
	"submitted":            "revision.submitted",
	"approval":             "revision.approved",
	"changes_requested":    "revision.changes_requested",
	"withdrawn":            "revision.withdrawn",
	"closed":               "revision.closed",
	"reopened":             "revision.reopened",
	"restored":             "revision.checkpoint_restored",
	"suggestions_accepted": "revision.suggestions_accepted",
	"suggestions_rejected": "revision.suggestions_rejected",
}

func auditEvent(ctx context.Context, q store.Querier, revisionID, actorType, actorID, kind string, data []byte) error {
	action, ok := audited[kind]
	if !ok {
		return nil
	}
	var repoID string
	var number int
	if err := store.QueryRow(ctx, q, `SELECT repo_id, number FROM revisions WHERE id = ?`, revisionID).Scan(&repoID, &number); err != nil {
		return err
	}
	d := map[string]any{}
	_ = json.Unmarshal(data, &d)
	d["number"] = number
	return audit.Write(ctx, q, audit.Entry{ActorType: actorType, ActorID: actorID, Action: action, TargetType: "revision", TargetID: revisionID, RepoID: repoID, Data: d})
}

// Events returns the revision's log, oldest first.
func Events(ctx context.Context, q store.Querier, revisionID string) ([]Event, error) {
	rows, err := store.Query(ctx, q, `SELECT e.id, e.actor_type, e.actor_id, COALESCE(u.name, ''), e.kind, e.data, e.created_at
		FROM revision_events e LEFT JOIN users u ON e.actor_type = 'user' AND u.id = e.actor_id
		WHERE e.revision_id = ? ORDER BY e.created_at, e.id`, revisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var data string
		var at int64
		if err := rows.Scan(&e.ID, &e.ActorType, &e.ActorID, &e.ActorName, &e.Kind, &data, &at); err != nil {
			return nil, err
		}
		e.Data, e.CreatedAt = json.RawMessage(data), store.FromMillis(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ErrInvalid is a validation failure on a field.
type ErrInvalid struct{ Field, Msg string }

func (e *ErrInvalid) Error() string { return e.Msg }

func invalid(field, msg string) error { return &ErrInvalid{Field: field, Msg: msg} }

// ErrConflict is a request that doesn't fit the revision's current state.
type ErrConflict struct{ Code, Msg string }

func (e *ErrConflict) Error() string { return e.Msg }

func conflict(code, msg string) error { return &ErrConflict{Code: code, Msg: msg} }

// ErrForbidden means the caller can't do this.
var ErrForbidden = errors.New("revisions: forbidden")
