// Package notify delivers inbox items and browser pushes to the people
// involved in revisions and threads (docs/specs/13-operations.md#notifications).
// There are no notification emails.
package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/jobs"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/threads"
	"github.com/kmdn-app/kmdn/internal/users"
)

// JobDrain turns new revision events into notifications.
const JobDrain = "notify.drain"

// Kinds, each with its own preferences.
const (
	KindMention         = "mention"
	KindReply           = "reply"
	KindReviewRequested = "review_requested"
	KindApproval        = "approval"
	KindApproved        = "approved"
	KindChanges         = "changes_requested"
	KindUpdate          = "update_available"
	KindConflicts       = "conflicts"
	KindPublishing      = "publishing"
	KindPublished       = "published"
	KindPublishClosed   = "publish_closed"
	KindInvited         = "invited"
	KindPagePublished   = "page_published"
)

// Kinds lists every kind (for preferences).
var Kinds = []string{KindMention, KindReply, KindReviewRequested, KindApproval, KindApproved, KindChanges, KindUpdate, KindConflicts, KindPublishing, KindPublished, KindPublishClosed, KindInvited, KindPagePublished}

// PushWindow coalesces pushes: at most one per revision per person in it.
var PushWindow = 5 * time.Minute

// Service delivers notifications.
type Service struct {
	DB   *store.DB
	Jobs *jobs.Queue
	// PublishUser sends a live event to a user's sessions (realtime.Hub.PublishUser).
	PublishUser func(userID string, event map[string]any)
	// Present reports whether the user has the revision open (no push then).
	Present func(revisionID, userID string) bool
	// Push sends browser pushes; nil disables them.
	Push *Pusher
	// Repos reads history for "updated since your last visit".
	Repos *repos.Service
	// OnEvent also sees each revision event once (outgoing webhooks).
	OnEvent func(ctx context.Context, rev revisions.Revision, actorID, kind string, data json.RawMessage) error
	BaseURL string
	Log     *slog.Logger

	mu       sync.Mutex
	lastPush map[string]time.Time // user|revision → last push
	pushing  sync.WaitGroup
}

// Wait blocks until pushes in flight are sent (tests, shutdown).
func (s *Service) Wait() { s.pushing.Wait() }

// Notification is an inbox item.
type Notification struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	RepoID     string          `json:"repo_id,omitempty"`
	RevisionID string          `json:"revision_id,omitempty"`
	ThreadID   string          `json:"thread_id,omitempty"`
	ActorID    string          `json:"actor_id,omitempty"`
	ActorName  string          `json:"actor_name,omitempty"`
	Data       json.RawMessage `json:"data"`
	CreatedAt  time.Time       `json:"created_at"`
	ReadAt     *time.Time      `json:"read_at,omitempty"`

	userID string
}

// Data common to notifications, for display and links.
type Data struct {
	Title   string `json:"title,omitempty"` // revision title
	Number  int    `json:"number,omitempty"`
	Org     string `json:"org,omitempty"`   // repo slug parts, for links
	Owner   string `json:"owner,omitempty"` // (Org is absent in items from before orgs)
	Repo    string `json:"repo,omitempty"`
	Path    string `json:"path,omitempty"`
	Quote   string `json:"quote,omitempty"`
	Excerpt string `json:"excerpt,omitempty"`
	// Approval progress ("Approved 1 of 2").
	Approved  int    `json:"approved,omitempty"`
	Of        int    `json:"of,omitempty"`
	Conflicts int    `json:"conflicts,omitempty"`
	URL       string `json:"url,omitempty"` // pull/merge request
}

// Register wires the drain job.
func (s *Service) Register() {
	s.Jobs.Register(JobDrain, func(ctx context.Context, _ jobs.Job) (any, error) { return nil, s.Drain(ctx) })
}

// Kick schedules a drain (deduplicated).
func (s *Service) Kick(ctx context.Context) {
	if _, err := s.Jobs.Enqueue(ctx, s.DB, JobDrain, map[string]string{}, jobs.EnqueueOptions{Key: JobDrain}); err != nil && s.Log != nil {
		s.Log.Error("enqueue notify drain", "err", err)
	}
}

type prefs struct{ inApp, push bool }

func (s *Service) prefsFor(ctx context.Context, userID, kind string) prefs {
	p := prefs{true, true}
	_ = store.QueryRow(ctx, s.DB, `SELECT in_app, push FROM notification_prefs WHERE user_id = ? AND kind = ?`, userID, kind).Scan(&p.inApp, &p.push)
	return p
}

// deliver stores the notification for each recipient (but the actor) and
// pushes it where wanted.
func (s *Service) deliver(ctx context.Context, n Notification, to []string) {
	seen := map[string]bool{n.ActorID: true}
	if n.ActorID != "" && n.ActorName == "" {
		if u, err := users.ByID(ctx, s.DB, n.ActorID); err == nil {
			n.ActorName = u.Name
		}
	}
	for _, uid := range to {
		if uid == "" || seen[uid] {
			continue
		}
		seen[uid] = true
		if !s.canReceive(ctx, uid, n.RepoID) {
			continue
		}
		p := s.prefsFor(ctx, uid, n.Kind)
		if !p.inApp && !p.push {
			continue
		}
		m := n
		m.ID, m.userID, m.CreatedAt = ids.New("ntf"), uid, time.Now()
		if p.inApp {
			if _, err := store.Exec(ctx, s.DB, `INSERT INTO notifications (id, user_id, kind, repo_id, revision_id, thread_id, actor_id, data, created_at, org_id)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, COALESCE((SELECT org_id FROM repos WHERE id = ?), 'org_default'))`,
				m.ID, uid, m.Kind, nullable(m.RepoID), nullable(m.RevisionID), nullable(m.ThreadID), nullable(m.ActorID), string(m.Data), store.Millis(m.CreatedAt), m.RepoID); err != nil {
				s.Log.Error("store notification", "err", err, "user", uid)
				continue
			}
			if s.PublishUser != nil {
				s.PublishUser(uid, map[string]any{"type": "notification", "kind": m.Kind, "id": m.ID})
			}
		}
		if p.push && s.Push != nil && s.pushAllowed(uid, m.RevisionID) {
			msg := s.pushMessage(m)
			s.pushing.Add(1)
			go func() {
				defer s.pushing.Done()
				pushCtx := context.WithoutCancel(ctx)
				if s.canReceive(pushCtx, uid, m.RepoID) {
					s.Push.Send(pushCtx, uid, msg)
				}
			}()
		}
	}
}

// Historical participation does not grant access to new notifications.
func (s *Service) canReceive(ctx context.Context, userID, repoID string) bool {
	u, err := users.ByID(ctx, s.DB, userID)
	if err != nil || u.Status != users.Active {
		return false
	}
	if repoID == "" {
		return true
	}
	role, err := access.Effective(ctx, s.DB, u, repoID)
	return err == nil && role != access.None
}

// pushAllowed: not while they have the revision open, and at most one push
// per revision per PushWindow.
func (s *Service) pushAllowed(userID, revID string) bool {
	if revID != "" && s.Present != nil && s.Present(revID, userID) {
		return false
	}
	key := userID + "|" + revID
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastPush == nil {
		s.lastPush = map[string]time.Time{}
	}
	if revID != "" {
		if at, ok := s.lastPush[key]; ok && time.Since(at) < PushWindow {
			return false
		}
	}
	s.lastPush[key] = time.Now()
	return true
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// Drain turns revision events not yet seen into notifications.
func (s *Service) Drain(ctx context.Context) error {
	for {
		rows, err := store.Query(ctx, s.DB, `SELECT id, revision_id, actor_type, actor_id, kind, data FROM revision_events WHERE notified = FALSE ORDER BY created_at, id LIMIT 100`)
		if err != nil {
			return err
		}
		type ev struct{ id, rev, actorType, actor, kind, data string }
		var list []ev
		for rows.Next() {
			var e ev
			if err := rows.Scan(&e.id, &e.rev, &e.actorType, &e.actor, &e.kind, &e.data); err != nil {
				rows.Close()
				return err
			}
			list = append(list, e)
		}
		rows.Close()
		if len(list) == 0 {
			return nil
		}
		for _, e := range list {
			actor := ""
			if e.actorType == revisions.ActorUser {
				actor = e.actor
			}
			if err := s.revisionEvent(ctx, e.rev, actor, e.kind, []byte(e.data)); err != nil {
				s.Log.Error("notify revision event", "err", err, "event", e.id, "kind", e.kind)
			}
			if _, err := store.Exec(ctx, s.DB, `UPDATE revision_events SET notified = TRUE WHERE id = ?`, e.id); err != nil {
				return err
			}
		}
	}
}

// revisionEvent maps a revision event to who hears about it.
func (s *Service) revisionEvent(ctx context.Context, revID, actor, kind string, raw []byte) error {
	rev, err := revisions.Get(ctx, s.DB, revID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	if s.OnEvent != nil {
		if err := s.OnEvent(ctx, rev, actor, kind, raw); err != nil {
			s.Log.Error("revision event hook", "err", err, "kind", kind)
		}
	}
	repo, err := repos.Get(ctx, s.DB, rev.RepoID)
	if err != nil {
		return err
	}
	members, err := revisions.Members(ctx, s.DB, rev.ID)
	if err != nil {
		return err
	}
	reviewers, err := revisions.Reviewers(ctx, s.DB, rev)
	if err != nil {
		return err
	}
	var editors, revs []string
	for _, m := range members {
		editors = append(editors, m.UserID)
	}
	approved := 0
	for _, r := range reviewers {
		revs = append(revs, r.UserID)
		if r.State == revisions.ApprovalApproved {
			approved++
		}
	}
	var data struct {
		UserID    string `json:"user_id"`
		Conflicts int    `json:"conflicts"`
		URL       string `json:"url"`
		Note      string `json:"note"`
	}
	_ = json.Unmarshal(raw, &data)
	d := Data{Title: rev.Title, Number: rev.Number, Org: repo.OrgSlug, Owner: repo.Owner, Repo: repo.Name}
	n := Notification{RepoID: repo.ID, RevisionID: rev.ID, ActorID: actor}
	send := func(k string, to ...[]string) {
		n.Kind, n.Data = k, mustJSON(d)
		var all []string
		for _, l := range to {
			all = append(all, l...)
		}
		s.deliver(ctx, n, all)
	}
	switch kind {
	case "submitted":
		send(KindReviewRequested, revs)
	case "reviewer_added":
		if rev.State == revisions.InReview || rev.State == revisions.Approved {
			send(KindReviewRequested, []string{data.UserID})
		}
	case "approval":
		d.Approved, d.Of = approved, len(reviewers)
		send(KindApproval, editors, revs)
	case "approved":
		send(KindApproved, editors, revs)
	case "changes_requested":
		d.Excerpt = excerpt(data.Note)
		send(KindChanges, editors, revs)
	case "update_available":
		if rev.State == revisions.Editing {
			send(KindUpdate, editors)
		} else {
			send(KindUpdate, revs)
		}
	case "updates_applied":
		if data.Conflicts > 0 {
			d.Conflicts = data.Conflicts
			send(KindConflicts, editors, revs)
		}
	case "publishing":
		d.URL = data.URL
		send(KindPublishing, editors, revs)
	case "published":
		send(KindPublished, editors, revs)
		d.Excerpt = rev.Title // the change summary (the assistant writes one when configured)
		return s.published(ctx, rev, n, d, append(editors, revs...))
	case "change_request_closed":
		send(KindPublishClosed, editors, revs)
	case "member_added":
		send(KindInvited, []string{data.UserID})
	}
	return nil
}

func excerpt(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 140 {
		return string(r[:139]) + "…"
	}
	return s
}

var mentionRe = regexp.MustCompile(`(?:^|[^\w@.])@([A-Za-z0-9][A-Za-z0-9._-]*)`)

// Mentions finds the users a comment @mentions (by the part of their email
// before the @, or their name without spaces) who can see the repository.
func (s *Service) Mentions(ctx context.Context, repoID, body string) ([]users.User, error) {
	var handles []string
	for _, m := range mentionRe.FindAllStringSubmatch(body, 20) {
		handles = append(handles, strings.ToLower(strings.TrimRight(m[1], ".-_")))
	}
	if len(handles) == 0 {
		return nil, nil
	}
	rows, err := store.Query(ctx, s.DB, `SELECT id, name, email FROM users WHERE status = 'active'`)
	if err != nil {
		return nil, err
	}
	type cand struct{ id, name, email string }
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.name, &c.email); err != nil {
			rows.Close()
			return nil, err
		}
		cands = append(cands, c)
	}
	rows.Close()
	var out []users.User
	for _, c := range cands {
		local := strings.ToLower(strings.SplitN(c.email, "@", 2)[0])
		name := strings.ToLower(strings.ReplaceAll(c.name, " ", ""))
		for _, h := range handles {
			if h != local && (name == "" || h != name) {
				continue
			}
			if u, err := users.ByID(ctx, s.DB, c.id); err == nil {
				if role, err := access.Effective(ctx, s.DB, u, repoID); err == nil && role != access.None {
					out = append(out, u)
				}
			}
			break
		}
	}
	return out, nil
}

// Comment notifies thread participants of a new comment and anyone it
// @mentions (a mention wins over the reply notification).
func (s *Service) Comment(ctx context.Context, t threads.Thread, c threads.Comment, by users.User) {
	d := Data{Path: t.Path, Excerpt: excerpt(c.Body)}
	var a threads.Anchor
	_ = json.Unmarshal(t.Anchor, &a)
	d.Quote = excerpt(a.Quote)
	if repo, err := repos.Get(ctx, s.DB, t.RepoID); err == nil {
		d.Org, d.Owner, d.Repo = repo.OrgSlug, repo.Owner, repo.Name
	}
	if t.RevisionID != "" {
		if rev, err := revisions.Get(ctx, s.DB, t.RevisionID); err == nil {
			d.Title, d.Number = rev.Title, rev.Number
		}
	}
	n := Notification{RepoID: t.RepoID, RevisionID: t.RevisionID, ThreadID: t.ID, ActorID: by.ID, Data: mustJSON(d)}
	mentioned := map[string]bool{}
	if ms, err := s.Mentions(ctx, t.RepoID, c.Body); err == nil {
		var to []string
		for _, u := range ms {
			mentioned[u.ID] = true
			to = append(to, u.ID)
		}
		n.Kind = KindMention
		s.deliver(ctx, n, to)
	}
	var participants []string
	for _, x := range t.Comments {
		if x.AuthorID != "" && !mentioned[x.AuthorID] {
			participants = append(participants, x.AuthorID)
		}
	}
	if t.CreatedBy != "" && !mentioned[t.CreatedBy] {
		participants = append(participants, t.CreatedBy)
	}
	n.Kind = KindReply
	s.deliver(ctx, n, participants)
}

// List returns a user's notifications, newest first, and how many are unread.
func List(ctx context.Context, q store.Querier, userID string, unreadOnly bool, limit int) ([]Notification, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	where := "n.user_id = ?"
	args := []any{userID}
	u, err := users.ByID(ctx, q, userID)
	if err != nil {
		return nil, 0, err
	}
	if u.Status != users.Active {
		return []Notification{}, 0, nil
	}
	if !u.IsInstanceAdmin {
		where += ` AND (n.repo_id IS NULL OR EXISTS (
			SELECT 1 FROM repo_members m WHERE m.repo_id = n.repo_id AND (
			(m.principal_type = 'user' AND m.principal_id = ?) OR
			(m.principal_type = 'group' AND m.principal_id IN (SELECT group_id FROM group_members WHERE user_id = ?)))))`
		args = append(args, userID, userID)
	}
	unreadWhere := where + " AND n.read_at IS NULL"
	if unreadOnly {
		where += " AND n.read_at IS NULL"
	}
	rows, err := store.Query(ctx, q, `SELECT n.id, n.kind, COALESCE(n.repo_id, ''), COALESCE(n.revision_id, ''), COALESCE(n.thread_id, ''), COALESCE(n.actor_id, ''), COALESCE(u.name, ''), n.data, n.created_at, n.read_at
		FROM notifications n LEFT JOIN users u ON u.id = n.actor_id WHERE `+where+` ORDER BY n.created_at DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Notification{}
	for rows.Next() {
		var n Notification
		var data string
		var created int64
		var read sql.NullInt64
		if err := rows.Scan(&n.ID, &n.Kind, &n.RepoID, &n.RevisionID, &n.ThreadID, &n.ActorID, &n.ActorName, &data, &created, &read); err != nil {
			return nil, 0, err
		}
		n.Data, n.CreatedAt, n.ReadAt = json.RawMessage(data), store.FromMillis(created), store.NullMillis(read)
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var unread int
	err = store.QueryRow(ctx, q, `SELECT COUNT(*) FROM notifications n WHERE `+unreadWhere, args...).Scan(&unread)
	return out, unread, err
}

// MarkRead marks some (or, with no ids, all) of a user's notifications read.
func MarkRead(ctx context.Context, q store.Querier, userID string, idList []string) error {
	now := store.Millis(time.Now())
	if len(idList) == 0 {
		_, err := store.Exec(ctx, q, `UPDATE notifications SET read_at = ? WHERE user_id = ? AND read_at IS NULL`, now, userID)
		return err
	}
	for _, id := range idList {
		if _, err := store.Exec(ctx, q, `UPDATE notifications SET read_at = ? WHERE id = ? AND user_id = ? AND read_at IS NULL`, now, id, userID); err != nil {
			return err
		}
	}
	return nil
}

// Purge drops notifications older than 90 days.
func Purge(ctx context.Context, q store.Querier) error {
	_, err := store.Exec(ctx, q, `DELETE FROM notifications WHERE created_at < ?`, store.Millis(time.Now().Add(-90*24*time.Hour)))
	return err
}
