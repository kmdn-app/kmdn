package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/llm"
	"github.com/kmdn-app/kmdn/internal/store"
)

// Thread is a conversation: private Q&A (OwnerID) or a revision's shared one.
type Thread struct {
	ID         string    `json:"id"`
	RepoID     string    `json:"repo_id"`
	RevisionID string    `json:"revision_id,omitempty"`
	OwnerID    string    `json:"owner_id,omitempty"`
	Title      string    `json:"title"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Context is where the person was when they asked.
type Context struct {
	Path      string `json:"path,omitempty"`
	Selection string `json:"selection,omitempty"`
}

// Message is a stored turn.
type Message struct {
	ID         string      `json:"id"`
	Role       string      `json:"role"`
	AuthorID   string      `json:"author_id,omitempty"`
	AuthorName string      `json:"author_name,omitempty"`
	Content    []llm.Block `json:"content"`
	Context    Context     `json:"context"`
	RunID      string      `json:"run_id,omitempty"`
	CreatedAt  time.Time   `json:"created_at"`
}

// human reports whether a person wrote it (not tool results).
func (m Message) human() bool {
	if m.Role != llm.RoleUser {
		return false
	}
	for _, b := range m.Content {
		if b.Type == llm.BlockToolResult {
			return false
		}
	}
	return true
}

const threadCols = `id, repo_id, COALESCE(revision_id, ''), COALESCE(owner_id, ''), title, created_at, updated_at`

func scanThread(row interface{ Scan(...any) error }) (Thread, error) {
	var t Thread
	var c, u int64
	if err := row.Scan(&t.ID, &t.RepoID, &t.RevisionID, &t.OwnerID, &t.Title, &c, &u); err != nil {
		return t, store.NotFound(err)
	}
	t.CreatedAt, t.UpdatedAt = store.FromMillis(c), store.FromMillis(u)
	return t, nil
}

// GetThread loads a thread.
func GetThread(ctx context.Context, q store.Querier, id string) (Thread, error) {
	return scanThread(store.QueryRow(ctx, q, `SELECT `+threadCols+` FROM assistant_threads WHERE id = ?`, id))
}

// RevisionThread returns the revision's shared thread, creating it the first time.
func RevisionThread(ctx context.Context, db *store.DB, repoID, revisionID string) (Thread, error) {
	t, err := scanThread(store.QueryRow(ctx, db, `SELECT `+threadCols+` FROM assistant_threads WHERE revision_id = ?`, revisionID))
	if err == nil || !errors.Is(err, store.ErrNotFound) {
		return t, err
	}
	now := time.Now()
	t = Thread{ID: ids.New("ath"), RepoID: repoID, RevisionID: revisionID, CreatedAt: now, UpdatedAt: now}
	_, err = store.Exec(ctx, db, `INSERT INTO assistant_threads (id, repo_id, revision_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, t.ID, repoID, revisionID, store.Millis(now), store.Millis(now))
	if err != nil {
		return t, err
	}
	return scanThread(store.QueryRow(ctx, db, `SELECT `+threadCols+` FROM assistant_threads WHERE revision_id = ?`, revisionID))
}

// CreateQA starts a private Q&A thread.
func CreateQA(ctx context.Context, q store.Querier, repoID, ownerID, title string) (Thread, error) {
	now := time.Now()
	t := Thread{ID: ids.New("ath"), RepoID: repoID, OwnerID: ownerID, Title: title, CreatedAt: now, UpdatedAt: now}
	_, err := store.Exec(ctx, q, `INSERT INTO assistant_threads (id, repo_id, owner_id, title, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, t.ID, repoID, ownerID, title, store.Millis(now), store.Millis(now))
	return t, err
}

// ListQA returns a person's Q&A threads in a repository, most recent first.
func ListQA(ctx context.Context, q store.Querier, repoID, ownerID string) ([]Thread, error) {
	rows, err := store.Query(ctx, q, `SELECT `+threadCols+` FROM assistant_threads WHERE repo_id = ? AND owner_id = ? ORDER BY updated_at DESC LIMIT 50`, repoID, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Thread{}
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// AddMessage appends a message to a thread.
func AddMessage(ctx context.Context, q store.Querier, threadID string, m Message) (Message, error) {
	if m.ID == "" {
		m.ID = ids.New("amsg")
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	content, _ := json.Marshal(m.Content)
	cx, _ := json.Marshal(m.Context)
	var author, run any
	if m.AuthorID != "" {
		author = m.AuthorID
	}
	if m.RunID != "" {
		run = m.RunID
	}
	if _, err := store.Exec(ctx, q, `INSERT INTO assistant_messages (id, thread_id, role, author_id, content, context, run_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, threadID, m.Role, author, string(content), string(cx), run, store.Millis(m.CreatedAt)); err != nil {
		return m, err
	}
	_, err := store.Exec(ctx, q, `UPDATE assistant_threads SET updated_at = ? WHERE id = ?`, store.Millis(m.CreatedAt), threadID)
	return m, err
}

// Messages returns a thread's messages in order.
func Messages(ctx context.Context, q store.Querier, threadID string) ([]Message, error) {
	rows, err := store.Query(ctx, q, `SELECT m.id, m.role, COALESCE(m.author_id, ''), COALESCE(u.name, ''), m.content, m.context, COALESCE(m.run_id, ''), m.created_at
		FROM assistant_messages m LEFT JOIN users u ON u.id = m.author_id WHERE m.thread_id = ? ORDER BY m.created_at, m.id`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		var content, cx string
		var at int64
		if err := rows.Scan(&m.ID, &m.Role, &m.AuthorID, &m.AuthorName, &content, &cx, &m.RunID, &at); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(content), &m.Content)
		_ = json.Unmarshal([]byte(cx), &m.Context)
		m.CreatedAt = store.FromMillis(at)
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteThread removes a thread and its messages.
func DeleteThread(ctx context.Context, q store.Querier, id string) error {
	_, err := store.Exec(ctx, q, `DELETE FROM assistant_threads WHERE id = ?`, id)
	return err
}

// PeopleSaid returns what people wrote in a revision's assistant thread (the
// intent behind its changes), most recent last, bounded.
func PeopleSaid(ctx context.Context, db *store.DB, revisionID string) string {
	t, err := scanThread(store.QueryRow(ctx, db, `SELECT `+threadCols+` FROM assistant_threads WHERE revision_id = ?`, revisionID))
	if err != nil {
		return ""
	}
	msgs, err := Messages(ctx, db, t.ID)
	if err != nil {
		return ""
	}
	var lines []string
	for _, m := range msgs {
		if !m.human() {
			continue
		}
		for _, b := range m.Content {
			if b.Type == llm.BlockText && b.Text != "" {
				lines = append(lines, nameOr(m.AuthorName)+": "+b.Text)
			}
		}
	}
	if len(lines) > 12 {
		lines = lines[len(lines)-12:]
	}
	out := strings.Join(lines, "\n")
	if len(out) > 6000 {
		out = out[len(out)-6000:]
	}
	return out
}
