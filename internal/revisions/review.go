package revisions

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

// Approval states.
const (
	ApprovalApproved         = "approved"
	ApprovalChangesRequested = "changes_requested"
)

// Reviewer is an assigned reviewer and where they stand this round.
type Reviewer struct {
	UserID string `json:"user_id"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	// State: pending, approved or changes_requested (this review round).
	State      string     `json:"state"`
	ApprovedAt *time.Time `json:"approved_at,omitempty"`
}

// IsReviewer reports whether the user is an assigned (not removed) reviewer.
func IsReviewer(ctx context.Context, q store.Querier, revisionID, userID string) (bool, error) {
	var n int
	err := store.QueryRow(ctx, q, `SELECT COUNT(*) FROM revision_reviewers WHERE revision_id = ? AND user_id = ? AND removed_at IS NULL`, revisionID, userID).Scan(&n)
	return n > 0, err
}

// Reviewers lists assigned reviewers with their state in the current round.
func Reviewers(ctx context.Context, q store.Querier, rev Revision) ([]Reviewer, error) {
	rows, err := store.Query(ctx, q, `SELECT r.user_id, u.name, u.email,
		(SELECT a.state FROM approvals a WHERE a.revision_id = r.revision_id AND a.user_id = r.user_id AND a.review_round = ? AND a.dismissed_at IS NULL ORDER BY a.created_at DESC LIMIT 1),
		(SELECT a.created_at FROM approvals a WHERE a.revision_id = r.revision_id AND a.user_id = r.user_id AND a.review_round = ? AND a.dismissed_at IS NULL AND a.state = 'approved' ORDER BY a.created_at DESC LIMIT 1)
		FROM revision_reviewers r JOIN users u ON u.id = r.user_id
		WHERE r.revision_id = ? AND r.removed_at IS NULL ORDER BY u.name`, rev.ReviewRound, rev.ReviewRound, rev.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Reviewer{}
	for rows.Next() {
		var r Reviewer
		var state sql.NullString
		var at sql.NullInt64
		if err := rows.Scan(&r.UserID, &r.Name, &r.Email, &state, &at); err != nil {
			return nil, err
		}
		r.State = "pending"
		if state.Valid {
			r.State = state.String
		}
		r.ApprovedAt = store.NullMillis(at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ContentHash identifies the revision's content: every manifest entry's path,
// operation and materialized content. Approvals pin it.
func ContentHash(ctx context.Context, q store.Querier, revisionID string) (string, error) {
	files, err := Files(ctx, q, revisionID)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, f := range files {
		h.Write([]byte(f.Path + "\x00" + f.Op + "\x00" + f.FromPath + "\x00" + f.ContentHash + "\n"))
	}
	var assets []string
	rows, err := store.Query(ctx, q, `SELECT path, sha256 FROM revision_assets WHERE revision_id = ? ORDER BY path`, revisionID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var p, sha string
		if err := rows.Scan(&p, &sha); err != nil {
			return "", err
		}
		assets = append(assets, p+"\x00"+sha)
	}
	h.Write([]byte(strings.Join(assets, "\n")))
	return hex.EncodeToString(h.Sum(nil)[:16]), rows.Err()
}

// SavedHash is the content hash of the revision's last Save all ("" before the first).
func SavedHash(ctx context.Context, q store.Querier, revisionID string) (string, error) {
	var h string
	err := store.QueryRow(ctx, q, `SELECT content_hash FROM revision_checkpoints WHERE revision_id = ? AND commit_sha <> '' ORDER BY created_at DESC, id DESC LIMIT 1`, revisionID).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return h, err
}

// Unsaved reports whether the revision's content differs from its last
// Save all (before the first one: whether it has any change at all).
func Unsaved(ctx context.Context, q store.Querier, revisionID string) (bool, error) {
	saved, err := SavedHash(ctx, q, revisionID)
	if err != nil {
		return false, err
	}
	if saved == "" {
		var n int
		err := store.QueryRow(ctx, q, `SELECT (SELECT COUNT(*) FROM revision_files WHERE revision_id = ?) + (SELECT COUNT(*) FROM revision_assets WHERE revision_id = ?)`, revisionID, revisionID).Scan(&n)
		return n > 0, err
	}
	now, err := ContentHash(ctx, q, revisionID)
	return now != saved, err
}

// ReviewerCandidate is a maintainer who could review.
type ReviewerCandidate struct {
	UserID string `json:"user_id"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	// Suggested: maintains the touched folders (reviewed changes there recently).
	Suggested bool `json:"suggested"`
}

// Maintainers are the users with at least the maintainer role on the repo
// (directly or through groups), plus instance admins who are members.
func maintainers(ctx context.Context, q store.Querier, repoID string) ([]users.User, error) {
	rows, err := store.Query(ctx, q, `SELECT DISTINCT u.id FROM users u WHERE u.status = 'active' AND (
		u.id IN (SELECT principal_id FROM repo_members WHERE repo_id = ? AND principal_type = 'user' AND role IN ('maintainer', 'admin'))
		OR u.id IN (SELECT gm.user_id FROM group_members gm JOIN repo_members m ON m.principal_type = 'group' AND m.principal_id = gm.group_id
			WHERE m.repo_id = ? AND m.role IN ('maintainer', 'admin'))
		OR u.is_instance_admin)`, repoID, repoID)
	if err != nil {
		return nil, err
	}
	var idsList []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		idsList = append(idsList, id)
	}
	rows.Close()
	out := make([]users.User, 0, len(idsList))
	for _, id := range idsList {
		u, err := users.ByID(ctx, q, id)
		if err == nil {
			out = append(out, u)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ReviewerCandidates lists who can review, suggested ones first: maintainers
// who reviewed published changes in the touched folders over the last six
// months (Reviewed-by trailers), falling back to all maintainers.
func (s *Service) ReviewerCandidates(ctx context.Context, repo repos.Repo, rev Revision, editors map[string]bool) ([]ReviewerCandidate, error) {
	ms, err := maintainers(ctx, s.DB, repo.ID)
	if err != nil {
		return nil, err
	}
	reviewed := map[string]bool{} // emails
	if s.Repos != nil && repo.HeadSHA != "" {
		files, _ := Files(ctx, s.DB, rev.ID)
		since := time.Now().AddDate(0, -6, 0)
		seen := map[string]bool{}
		for _, f := range files {
			if seen[f.Path] {
				continue
			}
			seen[f.Path] = true
			commits, err := s.Repos.History(ctx, repo, f.Path, 30)
			if err != nil {
				continue
			}
			for _, c := range commits {
				if c.Date.Before(since) {
					continue
				}
				for _, p := range c.ReviewedBy {
					reviewed[strings.ToLower(p.Email)] = true
				}
			}
		}
	}
	allowSelf := repo.Settings.AllowSelfApproval
	out := []ReviewerCandidate{}
	for _, u := range ms {
		if editors[u.ID] && !allowSelf {
			continue
		}
		out = append(out, ReviewerCandidate{UserID: u.ID, Name: u.Name, Email: u.Email, Suggested: reviewed[strings.ToLower(u.Email)]})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Suggested && !out[j].Suggested })
	return out, nil
}

func (s *Service) editorSet(ctx context.Context, revID string) (map[string]bool, error) {
	ms, err := Members(ctx, s.DB, revID)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, m := range ms {
		out[m.UserID] = true
	}
	return out, nil
}

// checkReviewer validates a user as a reviewer of rev.
func (s *Service) checkReviewer(ctx context.Context, repo repos.Repo, rev Revision, userID string, editors map[string]bool) error {
	u, err := users.ByID(ctx, s.DB, userID)
	if err != nil {
		return invalid("reviewers", "Unknown reviewer.")
	}
	role, err := access.Effective(ctx, s.DB, u, repo.ID)
	if err != nil {
		return err
	}
	if !role.AtLeast(access.Maintainer) {
		return invalid("reviewers", u.Name+" isn't a maintainer of this repository, so they can't review.")
	}
	if editors[userID] && !repo.Settings.AllowSelfApproval {
		return invalid("reviewers", u.Name+" edits this revision, so they can't review it. (Repository settings can allow self-approval.)")
	}
	return nil
}

// SubmitInput sends a revision for review.
type SubmitInput struct {
	Reviewers   []string `json:"reviewers"`
	Title       *string  `json:"title,omitempty"`
	Description *string  `json:"description,omitempty"`
}

// Submitted is called after a revision goes in review (checkpoints hook in). Optional.
type Submitted func(ctx context.Context, rev Revision, by string)

// Submit moves an Editing revision to In review with at least one reviewer.
// Reviewers of a previous round who were asked for changes are kept unless
// the list says otherwise.
func (s *Service) Submit(ctx context.Context, repo repos.Repo, rev Revision, c Caller, in SubmitInput) (Revision, error) {
	a, err := s.AccessFor(ctx, rev, c)
	if err != nil {
		return rev, err
	}
	if rev.State != Editing {
		return rev, conflict("invalid_state", "Only revisions being edited can be submitted.")
	}
	if !a.CanSubmit {
		return rev, ErrForbidden
	}
	if rev.HasConflicts {
		return rev, conflict("conflicts_pending", "Resolve the conflicts before submitting.")
	}
	var n int
	if err := store.QueryRow(ctx, s.DB, `SELECT COUNT(*) FROM revision_files WHERE revision_id = ?`, rev.ID).Scan(&n); err != nil {
		return rev, err
	}
	if n == 0 {
		return rev, conflict("no_changes", "This revision doesn't change anything yet.")
	}
	reviewers := dedupe(in.Reviewers)
	if len(reviewers) == 0 {
		return rev, invalid("reviewers", "Choose at least one reviewer.")
	}
	editors, err := s.editorSet(ctx, rev.ID)
	if err != nil {
		return rev, err
	}
	for _, id := range reviewers {
		if err := s.checkReviewer(ctx, repo, rev, id, editors); err != nil {
			return rev, err
		}
	}
	if in.Title != nil || in.Description != nil {
		if rev, err = s.Update(ctx, rev, c, UpdateInput{Title: in.Title, Description: in.Description}); err != nil {
			return rev, err
		}
	}
	now := store.Millis(time.Now())
	err = s.DB.InTx(ctx, func(tx *store.Tx) error {
		res, err := store.Exec(ctx, tx, `UPDATE revisions SET state = ?, review_round = review_round + 1, submitted_at = ?, changes_requested = ? WHERE id = ? AND state = ?`,
			string(InReview), now, false, rev.ID, string(Editing))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return conflict("state_changed", "The revision changed state. Reload and try again.")
		}
		if err := s.setReviewers(ctx, tx, rev.ID, reviewers, c.User.ID, now); err != nil {
			return err
		}
		return Record(ctx, tx, rev.ID, ActorUser, c.User.ID, "submitted", map[string]any{"reviewers": reviewers})
	})
	if err != nil {
		return rev, err
	}
	next, err := Get(ctx, s.DB, rev.ID)
	if err != nil {
		return rev, err
	}
	if s.OnSubmitted != nil {
		s.OnSubmitted(ctx, next, c.User.ID)
	}
	s.changed(ctx, rev.ID, "submitted")
	return next, nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// setReviewers makes exactly these users the active reviewers.
func (s *Service) setReviewers(ctx context.Context, tx *store.Tx, revID string, reviewers []string, by string, now int64) error {
	keep := map[string]bool{}
	for _, id := range reviewers {
		keep[id] = true
		if _, err := store.Exec(ctx, tx, `INSERT INTO revision_reviewers (revision_id, user_id, requested_by, created_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (revision_id, user_id) DO UPDATE SET removed_at = NULL`, revID, id, by, now); err != nil {
			return err
		}
	}
	rows, err := store.Query(ctx, tx, `SELECT user_id FROM revision_reviewers WHERE revision_id = ? AND removed_at IS NULL`, revID)
	if err != nil {
		return err
	}
	var drop []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		if !keep[id] {
			drop = append(drop, id)
		}
	}
	rows.Close()
	for _, id := range drop {
		if _, err := store.Exec(ctx, tx, `UPDATE revision_reviewers SET removed_at = ? WHERE revision_id = ? AND user_id = ?`, now, revID, id); err != nil {
			return err
		}
	}
	return nil
}

// AddReviewer assigns another reviewer (while Editing, In review or Approved).
func (s *Service) AddReviewer(ctx context.Context, repo repos.Repo, rev Revision, c Caller, userID string) error {
	a, err := s.AccessFor(ctx, rev, c)
	if err != nil {
		return err
	}
	if !a.CanSubmit {
		return ErrForbidden
	}
	editors, err := s.editorSet(ctx, rev.ID)
	if err != nil {
		return err
	}
	if err := s.checkReviewer(ctx, repo, rev, userID, editors); err != nil {
		return err
	}
	now := store.Millis(time.Now())
	err = s.DB.InTx(ctx, func(tx *store.Tx) error {
		if _, err := store.Exec(ctx, tx, `INSERT INTO revision_reviewers (revision_id, user_id, requested_by, created_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (revision_id, user_id) DO UPDATE SET removed_at = NULL`, rev.ID, userID, c.User.ID, now); err != nil {
			return err
		}
		// A new reviewer hasn't approved: an Approved revision is in review again.
		if rev.State == Approved {
			if _, err := store.Exec(ctx, tx, `UPDATE revisions SET state = ? WHERE id = ? AND state = ?`, string(InReview), rev.ID, string(Approved)); err != nil {
				return err
			}
		}
		return Record(ctx, tx, rev.ID, ActorUser, c.User.ID, "reviewer_added", map[string]any{"user_id": userID})
	})
	if err == nil {
		s.changed(ctx, rev.ID, "reviewers")
	}
	return err
}

// RemoveReviewer unassigns a reviewer. Removing the last one while in review
// returns the revision to Editing; if everyone left has approved, it's Approved.
func (s *Service) RemoveReviewer(ctx context.Context, rev Revision, c Caller, userID string) error {
	a, err := s.AccessFor(ctx, rev, c)
	if err != nil {
		return err
	}
	if !a.CanSubmit && userID != c.User.ID {
		return ErrForbidden
	}
	now := store.Millis(time.Now())
	err = s.DB.InTx(ctx, func(tx *store.Tx) error {
		if _, err := store.Exec(ctx, tx, `UPDATE revision_reviewers SET removed_at = ? WHERE revision_id = ? AND user_id = ? AND removed_at IS NULL`, now, rev.ID, userID); err != nil {
			return err
		}
		if err := Record(ctx, tx, rev.ID, ActorUser, c.User.ID, "reviewer_removed", map[string]any{"user_id": userID}); err != nil {
			return err
		}
		return s.settle(ctx, tx, rev.ID)
	})
	if err == nil {
		s.changed(ctx, rev.ID, "reviewers")
	}
	return err
}

// settle moves an In review/Approved revision to the state its approvals say.
func (s *Service) settle(ctx context.Context, tx *store.Tx, revID string) error {
	rev, err := Get(ctx, tx, revID)
	if err != nil {
		return err
	}
	if rev.State != InReview && rev.State != Approved {
		return nil
	}
	rs, err := Reviewers(ctx, tx, rev)
	if err != nil {
		return err
	}
	next := Approved
	switch {
	case len(rs) == 0:
		next = Editing
	default:
		for _, r := range rs {
			if r.State != ApprovalApproved {
				next = InReview
				break
			}
		}
	}
	if next == rev.State {
		return nil
	}
	if _, err := store.Exec(ctx, tx, `UPDATE revisions SET state = ? WHERE id = ?`, string(next), revID); err != nil {
		return err
	}
	kind := "approved"
	switch next {
	case Editing:
		kind = "returned_to_editing"
	case InReview:
		kind = "in_review_again"
	}
	return Record(ctx, tx, revID, ActorSystem, "", kind, nil)
}

// Approve records the caller's approval of the current content.
func (s *Service) Approve(ctx context.Context, rev Revision, c Caller) (Revision, error) {
	a, err := s.AccessFor(ctx, rev, c)
	if err != nil {
		return rev, err
	}
	if !a.CanReview {
		return rev, ErrForbidden
	}
	if s.PendingUpdates != nil {
		if pending, err := s.PendingUpdates(ctx, rev); err != nil {
			return rev, err
		} else if pending {
			return rev, conflict("updates_pending", "Apply the updates from Published before approving.")
		}
	}
	if s.PendingSuggestions != nil {
		if n, err := s.PendingSuggestions(ctx, rev); err != nil {
			return rev, err
		} else if n > 0 {
			return rev, conflict("suggestions_pending", suggestionsPendingMsg(n))
		}
	}
	h, err := ContentHash(ctx, s.DB, rev.ID)
	if err != nil {
		return rev, err
	}
	err = s.DB.InTx(ctx, func(tx *store.Tx) error {
		now := store.Millis(time.Now())
		if _, err := store.Exec(ctx, tx, `UPDATE approvals SET dismissed_at = ? WHERE revision_id = ? AND user_id = ? AND review_round = ? AND dismissed_at IS NULL`, now, rev.ID, c.User.ID, rev.ReviewRound); err != nil {
			return err
		}
		if _, err := store.Exec(ctx, tx, `INSERT INTO approvals (id, revision_id, user_id, review_round, state, content_hash, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			ids.New("apr"), rev.ID, c.User.ID, rev.ReviewRound, ApprovalApproved, h, now); err != nil {
			return err
		}
		if err := Record(ctx, tx, rev.ID, ActorUser, c.User.ID, "approval", nil); err != nil {
			return err
		}
		return s.settle(ctx, tx, rev.ID)
	})
	if err != nil {
		return rev, err
	}
	s.changed(ctx, rev.ID, "approval")
	return Get(ctx, s.DB, rev.ID)
}

// RequestChanges sends the revision back to Editing with a note; approvals
// are dismissed and the same reviewers are asked again on resubmit.
func (s *Service) RequestChanges(ctx context.Context, rev Revision, c Caller, note string) (Revision, error) {
	a, err := s.AccessFor(ctx, rev, c)
	if err != nil {
		return rev, err
	}
	if !a.CanReview {
		return rev, ErrForbidden
	}
	note = strings.TrimSpace(note)
	err = s.DB.InTx(ctx, func(tx *store.Tx) error {
		now := store.Millis(time.Now())
		if _, err := store.Exec(ctx, tx, `UPDATE approvals SET dismissed_at = ? WHERE revision_id = ? AND review_round = ? AND dismissed_at IS NULL`, now, rev.ID, rev.ReviewRound); err != nil {
			return err
		}
		if _, err := store.Exec(ctx, tx, `INSERT INTO approvals (id, revision_id, user_id, review_round, state, note, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			ids.New("apr"), rev.ID, c.User.ID, rev.ReviewRound, ApprovalChangesRequested, note, now); err != nil {
			return err
		}
		res, err := store.Exec(ctx, tx, `UPDATE revisions SET state = ?, changes_requested = ? WHERE id = ? AND state IN (?, ?)`, string(Editing), true, rev.ID, string(InReview), string(Approved))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return conflict("state_changed", "The revision changed state. Reload and try again.")
		}
		return Record(ctx, tx, rev.ID, ActorUser, c.User.ID, "changes_requested", map[string]any{"note": note})
	})
	if err != nil {
		return rev, err
	}
	s.changed(ctx, rev.ID, "changes_requested")
	return Get(ctx, s.DB, rev.ID)
}

// Withdraw takes a revision out of review so its editors can change it.
func (s *Service) Withdraw(ctx context.Context, rev Revision, c Caller) (Revision, error) {
	a, err := s.AccessFor(ctx, rev, c)
	if err != nil {
		return rev, err
	}
	if !a.CanWithdraw {
		return rev, ErrForbidden
	}
	err = s.DB.InTx(ctx, func(tx *store.Tx) error {
		now := store.Millis(time.Now())
		res, err := store.Exec(ctx, tx, `UPDATE revisions SET state = ? WHERE id = ? AND state IN (?, ?)`, string(Editing), rev.ID, string(InReview), string(Approved))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return conflict("state_changed", "The revision changed state. Reload and try again.")
		}
		if _, err := store.Exec(ctx, tx, `UPDATE approvals SET dismissed_at = ? WHERE revision_id = ? AND review_round = ? AND dismissed_at IS NULL`, now, rev.ID, rev.ReviewRound); err != nil {
			return err
		}
		return Record(ctx, tx, rev.ID, ActorUser, c.User.ID, "withdrawn", nil)
	})
	if err != nil {
		return rev, err
	}
	s.changed(ctx, rev.ID, "withdrawn")
	return Get(ctx, s.DB, rev.ID)
}

// ContentChanged resets approvals after the revision's content changed while
// in review: everyone's but the people who made the change. An Approved
// revision goes back to In review.
func (s *Service) ContentChanged(ctx context.Context, revID string, by []string) error {
	rev, err := Get(ctx, s.DB, revID)
	if err != nil {
		return err
	}
	if rev.State != InReview && rev.State != Approved {
		return nil
	}
	h, err := ContentHash(ctx, s.DB, revID)
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	for _, u := range by {
		keep[u] = true
	}
	changed := false
	err = s.DB.InTx(ctx, func(tx *store.Tx) error {
		rows, err := store.Query(ctx, tx, `SELECT id, user_id, content_hash FROM approvals WHERE revision_id = ? AND review_round = ? AND state = 'approved' AND dismissed_at IS NULL`, revID, rev.ReviewRound)
		if err != nil {
			return err
		}
		type ap struct{ id, user, hash string }
		var list []ap
		for rows.Next() {
			var a ap
			if err := rows.Scan(&a.id, &a.user, &a.hash); err != nil {
				rows.Close()
				return err
			}
			list = append(list, a)
		}
		rows.Close()
		now := store.Millis(time.Now())
		for _, a := range list {
			switch {
			case a.hash == h:
				continue // same content (e.g. a no-op materialization)
			case keep[a.user]:
				// The editor's own change: their approval now covers it.
				if _, err := store.Exec(ctx, tx, `UPDATE approvals SET content_hash = ? WHERE id = ?`, h, a.id); err != nil {
					return err
				}
			default:
				if _, err := store.Exec(ctx, tx, `UPDATE approvals SET dismissed_at = ? WHERE id = ?`, now, a.id); err != nil {
					return err
				}
				changed = true
			}
		}
		if changed {
			if err := Record(ctx, tx, revID, ActorSystem, "", "approvals_reset", map[string]any{"by": by}); err != nil {
				return err
			}
		}
		return s.settle(ctx, tx, revID)
	})
	if err == nil && changed {
		s.changed(ctx, revID, "approvals_reset")
	}
	return err
}

func suggestionsPendingMsg(n int) string {
	if n == 1 {
		return "Accept or reject the pending suggestion before approving."
	}
	return fmt.Sprintf("Accept or reject the %d pending suggestions before approving.", n)
}
