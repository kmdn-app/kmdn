package revisions

import (
	"context"
	"time"

	"github.com/kmdn-app/kmdn/internal/store"
)

// MarkPublishing records the pull/merge request opened for a protected branch.
func (s *Service) MarkPublishing(ctx context.Context, revID, by, url, ref string) error {
	err := s.DB.InTx(ctx, func(tx *store.Tx) error {
		res, err := store.Exec(ctx, tx, `UPDATE revisions SET state = ?, change_request_url = ?, change_request_ref = ? WHERE id = ? AND state IN (?, ?)`,
			string(Publishing), url, ref, revID, string(Approved), string(Publishing))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return conflict("state_changed", "The revision changed state.")
		}
		return Record(ctx, tx, revID, ActorUser, by, "publishing", map[string]any{"url": url})
	})
	if err == nil {
		s.changed(ctx, revID, "publishing")
	}
	return err
}

// MarkPublished ends the revision: its content is on the target branch in sha.
func (s *Service) MarkPublished(ctx context.Context, revID, by, sha string) error {
	err := s.DB.InTx(ctx, func(tx *store.Tx) error {
		res, err := store.Exec(ctx, tx, `UPDATE revisions SET state = ?, published_sha = ?, published_at = ? WHERE id = ? AND state IN (?, ?)`,
			string(Published), sha, store.Millis(time.Now()), revID, string(Approved), string(Publishing))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil // already published (a resumed job, or the webhook came twice)
		}
		actor, id := ActorUser, by
		if by == "" {
			actor = ActorSystem
		}
		return Record(ctx, tx, revID, actor, id, "published", map[string]any{"sha": sha})
	})
	if err == nil {
		s.changed(ctx, revID, "published")
	}
	return err
}

// PublishStopped returns a Publishing revision to Approved (the pull request
// was closed unmerged) or an Approved one stays, with an event saying why.
func (s *Service) PublishStopped(ctx context.Context, revID, kind string, data map[string]any) error {
	err := s.DB.InTx(ctx, func(tx *store.Tx) error {
		if _, err := store.Exec(ctx, tx, `UPDATE revisions SET state = ? WHERE id = ? AND state = ?`, string(Approved), revID, string(Publishing)); err != nil {
			return err
		}
		return Record(ctx, tx, revID, ActorSystem, "", kind, data)
	})
	if err == nil {
		s.changed(ctx, revID, kind)
	}
	return err
}

// ByChangeRequest finds the revision publishing through a pull/merge request.
func ByChangeRequest(ctx context.Context, q store.Querier, repoID, ref string) (Revision, error) {
	return scanRevision(store.QueryRow(ctx, q, `SELECT `+revCols+` FROM revisions WHERE repo_id = ? AND change_request_ref = ? AND state = ?`, repoID, ref, string(Publishing)))
}
