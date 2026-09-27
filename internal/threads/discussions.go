package threads

import (
	"context"
	"strings"
	"time"
	"unicode"

	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
)

// SetFix links a discussion to the revision fixing it.
func SetFix(ctx context.Context, q store.Querier, threadID, revisionID string) error {
	_, err := store.Exec(ctx, q, `UPDATE threads SET fix_revision_id = ? WHERE id = ?`, revisionID, threadID)
	return err
}

// ResolveFixed resolves the discussions a published revision fixed and
// returns them.
func ResolveFixed(ctx context.Context, db *store.DB, revisionID, by string) ([]Thread, error) {
	list, err := listWhere(ctx, db, `fix_revision_id = ? AND state = ?`, revisionID, StateOpen)
	if err != nil || len(list) == 0 {
		return list, err
	}
	now := store.Millis(time.Now())
	var who any
	if by != "" {
		who = by
	}
	if _, err := store.Exec(ctx, db, `UPDATE threads SET state = ?, resolved_by = ?, resolved_at = ?, last_activity_at = ? WHERE fix_revision_id = ? AND state = ?`,
		StateResolved, who, now, now, revisionID, StateOpen); err != nil {
		return nil, err
	}
	return list, nil
}

func listWhere(ctx context.Context, q store.Querier, where string, args ...any) ([]Thread, error) {
	rows, err := store.Query(ctx, q, `SELECT `+threadCols+` FROM threads WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Thread
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// normalize folds whitespace and case, so a quote survives reflowed text.
func normalize(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// Found reports whether a discussion's quote is still in the page's text:
// exactly, then with whitespace and case folded.
func Found(text string, a Anchor) bool {
	if a.Quote == "" {
		return true
	}
	return strings.Contains(text, a.Quote) || strings.Contains(normalize(text), normalize(a.Quote))
}

// PageText returns a page's text as readers see it (used to re-anchor).
type PageText func(ctx context.Context, markdown string) (string, error)

// Reanchor checks the open discussions on pages Published changed between
// from and to: those whose quote is still there move to the new commit, the
// others are marked outdated (docs/specs/07-review.md#doc-discussions-published-docs).
func (s *Service) Reanchor(ctx context.Context, repo repos.Repo, from, to string) error {
	if s.Repos == nil || s.Text == nil || from == "" {
		return nil
	}
	changed, err := s.Repos.Mirror(repo).ChangedPaths(ctx, from, to)
	if err != nil {
		return err
	}
	for _, p := range changed {
		list, err := listWhere(ctx, s.DB, `repo_id = ? AND kind = ? AND path = ? AND state = ?`, repo.ID, KindDiscussion, p, StateOpen)
		if err != nil || len(list) == 0 {
			continue
		}
		text := ""
		if f, err := s.Repos.ReadFile(ctx, repo, p, to); err == nil {
			if text, err = s.Text(ctx, f.Content); err != nil {
				continue
			}
		}
		for _, t := range list {
			var a Anchor
			_ = jsonUnmarshal(t.Anchor, &a)
			found := text != "" && Found(text, a)
			if found {
				a.SHA = to
			}
			if _, err := store.Exec(ctx, s.DB, `UPDATE threads SET anchor = ?, outdated = ? WHERE id = ?`, jsonString(a), !found, t.ID); err != nil {
				return err
			}
			if found == t.Outdated {
				s.emit(t, "reanchored")
			}
		}
	}
	return nil
}

// RevisionPublished resolves the discussions a revision fixed.
func (s *Service) RevisionPublished(ctx context.Context, rev revisions.Revision) (int, error) {
	by := ""
	var actor string
	if err := store.QueryRow(ctx, s.DB, `SELECT actor_id FROM revision_events WHERE revision_id = ? AND kind = 'published' ORDER BY created_at DESC LIMIT 1`, rev.ID).Scan(&actor); err == nil {
		by = actor
	}
	list, err := ResolveFixed(ctx, s.DB, rev.ID, by)
	for _, t := range list {
		t.State = StateResolved
		s.emit(t, StateResolved)
	}
	return len(list), err
}
