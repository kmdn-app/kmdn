// Package threads holds comment threads: on a revision's pages (anchored in
// the page's Y.Doc so anchors move with the text) and, later, discussions on
// published pages. Bodies, authors and resolution live here, queryable and
// notifiable. See docs/specs/07-review.md#comments.
package threads

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/store"
)

// Kinds.
const (
	KindRevision   = "revision"
	KindDiscussion = "discussion"
)

// States.
const (
	StateOpen     = "open"
	StateResolved = "resolved"
)

// Reactions are a small fixed set.
var Reactions = map[string]bool{"+1": true, "check": true, "eyes": true}

// MaxBody bounds a comment.
const MaxBody = 20000

// Reaction is a kind and who used it.
type Reaction struct {
	Kind  string   `json:"kind"`
	Users []string `json:"users"`
}

// Comment is one message in a thread.
type Comment struct {
	ID         string     `json:"id"`
	AuthorID   string     `json:"author_id,omitempty"`
	AuthorName string     `json:"author_name,omitempty"`
	Body       string     `json:"body"`
	CreatedAt  time.Time  `json:"created_at"`
	EditedAt   *time.Time `json:"edited_at,omitempty"`
	Deleted    bool       `json:"deleted,omitempty"`
	Reactions  []Reaction `json:"reactions"`
}

// Thread is a conversation anchored to a passage.
type Thread struct {
	ID          string          `json:"id"`
	RepoID      string          `json:"repo_id"`
	RevisionID  string          `json:"revision_id,omitempty"`
	Path        string          `json:"path"`
	Kind        string          `json:"kind"`
	ReviewRound int             `json:"review_round"`
	Anchor      json.RawMessage `json:"anchor"`
	State       string          `json:"state"`
	CreatedBy   string          `json:"created_by,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	LastActive  time.Time       `json:"last_activity_at"`
	ResolvedBy  string          `json:"resolved_by,omitempty"`
	ResolvedAt  *time.Time      `json:"resolved_at,omitempty"`
	Comments    []Comment       `json:"comments"`
	// Outdated: a discussion whose quote Published no longer has.
	Outdated bool `json:"outdated,omitempty"`
	// FixRevision is the revision "Fix this" started for a discussion.
	FixRevision *RevisionRef `json:"fix_revision,omitempty"`
	// Hot is the recency-weighted activity score used by the Hot topics sort.
	Hot float64 `json:"hot"`
}

// RevisionRef points at a revision from a discussion.
type RevisionRef struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
	State  string `json:"state"`
}

// Anchor is what a thread points at. For revision threads the live position
// is in the Y.Doc (client-side); the quote survives if the text goes away.
type Anchor struct {
	Quote  string `json:"quote"`
	Prefix string `json:"prefix,omitempty"`
	Suffix string `json:"suffix,omitempty"`
	SHA    string `json:"sha,omitempty"` // discussions: published commit
}

// hot scores activity: Σ w·e^(−age/6h) over replies (w=1) and reactions
// (w=0.3), plus 0.5 per participant beyond the first.
func hot(t Thread, now time.Time) float64 {
	decay := func(at time.Time) float64 { return math.Exp(-now.Sub(at).Hours() / 6) }
	score := 0.0
	people := map[string]bool{}
	for i, c := range t.Comments {
		if i > 0 && !c.Deleted {
			score += decay(c.CreatedAt)
		}
		if c.AuthorID != "" {
			people[c.AuthorID] = true
		}
		for _, r := range c.Reactions {
			score += 0.3 * float64(len(r.Users)) * decay(c.CreatedAt)
		}
	}
	if len(people) > 1 {
		score += 0.5 * float64(len(people)-1)
	}
	return score
}

func scanThread(row interface{ Scan(...any) error }) (Thread, error) {
	var t Thread
	var rev, createdBy, resolvedBy, fix sql.NullString
	var anchor string
	var created, active int64
	var resolved sql.NullInt64
	err := row.Scan(&t.ID, &t.RepoID, &rev, &t.Path, &t.Kind, &t.ReviewRound, &anchor, &t.State, &createdBy, &created, &active, &resolvedBy, &resolved, &t.Outdated, &fix)
	if err != nil {
		return t, store.NotFound(err)
	}
	if fix.Valid && fix.String != "" {
		t.FixRevision = &RevisionRef{ID: fix.String}
	}
	t.RevisionID, t.CreatedBy, t.ResolvedBy = rev.String, createdBy.String, resolvedBy.String
	t.Anchor = json.RawMessage(anchor)
	t.CreatedAt, t.LastActive, t.ResolvedAt = store.FromMillis(created), store.FromMillis(active), store.NullMillis(resolved)
	return t, nil
}

const threadCols = `id, repo_id, revision_id, path, kind, review_round, anchor, state, created_by, created_at, last_activity_at, resolved_by, resolved_at, outdated, fix_revision_id`

// Get loads a thread with its comments.
func Get(ctx context.Context, q store.Querier, id string) (Thread, error) {
	t, err := scanThread(store.QueryRow(ctx, q, `SELECT `+threadCols+` FROM threads WHERE id = ?`, id))
	if err != nil {
		return t, err
	}
	list := []Thread{t}
	if err := fill(ctx, q, list); err != nil {
		return t, err
	}
	return list[0], nil
}

// RepoOf returns the repository a thread belongs to.
func RepoOf(ctx context.Context, q store.Querier, id string) (string, error) {
	var repoID string
	err := store.QueryRow(ctx, q, `SELECT repo_id FROM threads WHERE id = ?`, id).Scan(&repoID)
	return repoID, store.NotFound(err)
}

// Filter narrows a listing.
type Filter struct {
	RevisionID string
	RepoID     string
	Path       string
	Kind       string
	State      string // open | resolved (default: both)
	Sort       string // hot (default) | updated
}

// List returns threads with their comments, sorted.
func List(ctx context.Context, q store.Querier, f Filter) ([]Thread, error) {
	where, args := []string{"1 = 1"}, []any{}
	if f.RevisionID != "" {
		where, args = append(where, "revision_id = ?"), append(args, f.RevisionID)
	}
	if f.RepoID != "" {
		where, args = append(where, "repo_id = ?"), append(args, f.RepoID)
	}
	if f.Path != "" {
		where, args = append(where, "path = ?"), append(args, f.Path)
	}
	if f.Kind != "" {
		where, args = append(where, "kind = ?"), append(args, f.Kind)
	}
	if f.State != "" {
		where, args = append(where, "state = ?"), append(args, f.State)
	}
	rows, err := store.Query(ctx, q, `SELECT `+threadCols+` FROM threads WHERE `+strings.Join(where, " AND ")+` ORDER BY last_activity_at DESC LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	out := []Thread{}
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, t)
	}
	rows.Close()
	if err := fill(ctx, q, out); err != nil {
		return nil, err
	}
	now := time.Now()
	for i := range out {
		out[i].Hot = math.Round(hot(out[i], now)*1000) / 1000
	}
	if f.Sort != "updated" {
		sort.SliceStable(out, func(i, j int) bool {
			// Open threads before resolved ones, then by score.
			if (out[i].State == StateOpen) != (out[j].State == StateOpen) {
				return out[i].State == StateOpen
			}
			return out[i].Hot > out[j].Hot
		})
	}
	return out, nil
}

// fill loads comments and reactions for threads.
func fill(ctx context.Context, q store.Querier, list []Thread) error {
	if len(list) == 0 {
		return nil
	}
	for i := range list {
		if f := list[i].FixRevision; f != nil {
			_ = store.QueryRow(ctx, q, `SELECT number, state FROM revisions WHERE id = ?`, f.ID).Scan(&f.Number, &f.State)
		}
	}
	idx := map[string]int{}
	ph := make([]string, len(list))
	args := make([]any, len(list))
	for i, t := range list {
		idx[t.ID] = i
		list[i].Comments = []Comment{}
		ph[i], args[i] = "?", t.ID
	}
	rows, err := store.Query(ctx, q, `SELECT c.id, c.thread_id, COALESCE(c.author_id, ''), COALESCE(u.name, ''), c.body, c.created_at, c.edited_at, c.deleted_at
		FROM comments c LEFT JOIN users u ON u.id = c.author_id WHERE c.thread_id IN (`+strings.Join(ph, ", ")+`) ORDER BY c.created_at, c.id`, args...)
	if err != nil {
		return err
	}
	byComment := map[string][2]int{}
	for rows.Next() {
		var c Comment
		var threadID string
		var at int64
		var edited, deleted sql.NullInt64
		if err := rows.Scan(&c.ID, &threadID, &c.AuthorID, &c.AuthorName, &c.Body, &at, &edited, &deleted); err != nil {
			rows.Close()
			return err
		}
		c.CreatedAt, c.EditedAt = store.FromMillis(at), store.NullMillis(edited)
		c.Reactions = []Reaction{}
		if deleted.Valid {
			c.Deleted, c.Body = true, ""
		}
		i := idx[threadID]
		list[i].Comments = append(list[i].Comments, c)
		byComment[c.ID] = [2]int{i, len(list[i].Comments) - 1}
	}
	rows.Close()
	rrows, err := store.Query(ctx, q, `SELECT r.comment_id, r.kind, r.user_id FROM reactions r JOIN comments c ON c.id = r.comment_id
		WHERE c.thread_id IN (`+strings.Join(ph, ", ")+`) ORDER BY r.created_at`, args...)
	if err != nil {
		return err
	}
	defer rrows.Close()
	for rrows.Next() {
		var cid, kind, user string
		if err := rrows.Scan(&cid, &kind, &user); err != nil {
			return err
		}
		pos, ok := byComment[cid]
		if !ok {
			continue
		}
		c := &list[pos[0]].Comments[pos[1]]
		found := false
		for k := range c.Reactions {
			if c.Reactions[k].Kind == kind {
				c.Reactions[k].Users = append(c.Reactions[k].Users, user)
				found = true
			}
		}
		if !found {
			c.Reactions = append(c.Reactions, Reaction{Kind: kind, Users: []string{user}})
		}
	}
	return rrows.Err()
}

// CreateInput starts a thread.
type CreateInput struct {
	Path   string `json:"path"`
	Anchor Anchor `json:"anchor"`
	Body   string `json:"body"`
	// Position is the Yjs relative range {start, end} in the page's document;
	// it's stored in the document (so it moves with the text), not here.
	Position json.RawMessage `json:"position,omitempty"`
}

func cleanBody(s string) (string, bool) {
	s = strings.TrimSpace(s)
	return s, s != "" && utf8.RuneCountInString(s) <= MaxBody
}

// Create starts a thread with its first comment.
func Create(ctx context.Context, db *store.DB, repoID, revisionID, kind string, round int, by string, in CreateInput) (Thread, error) {
	body, ok := cleanBody(in.Body)
	if !ok {
		return Thread{}, errBody
	}
	if utf8.RuneCountInString(in.Anchor.Quote) > 2000 {
		r := []rune(in.Anchor.Quote)
		in.Anchor.Quote = string(r[:2000])
	}
	anchor, _ := json.Marshal(in.Anchor)
	now := store.Millis(time.Now())
	id := ids.New(ids.Thread)
	var rev any
	if revisionID != "" {
		rev = revisionID
	}
	err := db.InTx(ctx, func(tx *store.Tx) error {
		if _, err := store.Exec(ctx, tx, `INSERT INTO threads (id, repo_id, revision_id, path, kind, review_round, anchor, state, created_by, created_at, last_activity_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, repoID, rev, in.Path, kind, round, string(anchor), StateOpen, by, now, now); err != nil {
			return err
		}
		_, err := store.Exec(ctx, tx, `INSERT INTO comments (id, thread_id, author_id, body, created_at) VALUES (?, ?, ?, ?, ?)`, ids.New(ids.Comment), id, by, body, now)
		return err
	})
	if err != nil {
		return Thread{}, err
	}
	return Get(ctx, db, id)
}

// Reply adds a comment and reopens nothing (resolved threads stay resolved).
func Reply(ctx context.Context, db *store.DB, threadID, by, body string) (Comment, error) {
	body, ok := cleanBody(body)
	if !ok {
		return Comment{}, errBody
	}
	now := store.Millis(time.Now())
	id := ids.New(ids.Comment)
	err := db.InTx(ctx, func(tx *store.Tx) error {
		if _, err := store.Exec(ctx, tx, `INSERT INTO comments (id, thread_id, author_id, body, created_at) VALUES (?, ?, ?, ?, ?)`, id, threadID, by, body, now); err != nil {
			return err
		}
		_, err := store.Exec(ctx, tx, `UPDATE threads SET last_activity_at = ? WHERE id = ?`, now, threadID)
		return err
	})
	return Comment{ID: id, AuthorID: by, Body: body, CreatedAt: store.FromMillis(now), Reactions: []Reaction{}}, err
}

// SetState resolves or reopens a thread.
func SetState(ctx context.Context, db *store.DB, threadID, by, state string) error {
	now := store.Millis(time.Now())
	if state == StateResolved {
		_, err := store.Exec(ctx, db, `UPDATE threads SET state = ?, resolved_by = ?, resolved_at = ?, last_activity_at = ? WHERE id = ?`, StateResolved, by, now, now, threadID)
		return err
	}
	_, err := store.Exec(ctx, db, `UPDATE threads SET state = ?, resolved_by = NULL, resolved_at = NULL, last_activity_at = ? WHERE id = ?`, StateOpen, now, threadID)
	return err
}

// CommentInfo is who wrote a comment and where.
type CommentInfo struct {
	ThreadID string
	AuthorID string
	Deleted  bool
}

// CommentOf looks up a comment.
func CommentOf(ctx context.Context, q store.Querier, id string) (CommentInfo, error) {
	var ci CommentInfo
	var author sql.NullString
	var deleted sql.NullInt64
	err := store.QueryRow(ctx, q, `SELECT thread_id, author_id, deleted_at FROM comments WHERE id = ?`, id).Scan(&ci.ThreadID, &author, &deleted)
	ci.AuthorID, ci.Deleted = author.String, deleted.Valid
	return ci, store.NotFound(err)
}

// Edit changes a comment's body.
func Edit(ctx context.Context, db *store.DB, id, body string) error {
	body, ok := cleanBody(body)
	if !ok {
		return errBody
	}
	_, err := store.Exec(ctx, db, `UPDATE comments SET body = ?, edited_at = ? WHERE id = ? AND deleted_at IS NULL`, body, store.Millis(time.Now()), id)
	return err
}

// Delete soft-deletes a comment ("Comment deleted" stays in the thread).
func Delete(ctx context.Context, db *store.DB, id string) error {
	_, err := store.Exec(ctx, db, `UPDATE comments SET deleted_at = ?, body = '' WHERE id = ?`, store.Millis(time.Now()), id)
	return err
}

// React toggles a reaction.
func React(ctx context.Context, db *store.DB, commentID, userID, kind string, on bool) error {
	if on {
		_, err := store.Exec(ctx, db, `INSERT INTO reactions (comment_id, user_id, kind, created_at) VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING`, commentID, userID, kind, store.Millis(time.Now()))
		if err == nil {
			_, err = store.Exec(ctx, db, `UPDATE threads SET last_activity_at = ? WHERE id = (SELECT thread_id FROM comments WHERE id = ?)`, store.Millis(time.Now()), commentID)
		}
		return err
	}
	_, err := store.Exec(ctx, db, `DELETE FROM reactions WHERE comment_id = ? AND user_id = ? AND kind = ?`, commentID, userID, kind)
	return err
}

type bodyError struct{}

func (bodyError) Error() string { return "threads: empty or too long comment" }

var errBody error = bodyError{}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
