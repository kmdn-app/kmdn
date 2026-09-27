package publish

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/jobs"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
)

const JobRecover = "revision.publish.recover"

type claim struct {
	ID, RevisionID, BranchSHA, BranchBaseSHA, BaseSHA     string
	TargetBranch, TargetSHA, MergeSHA, Message, By, Phase string
	Objects                                               []byte
}

func (s *Service) loadClaim(ctx context.Context, revID string) (claim, bool, error) {
	var c claim
	err := store.QueryRow(ctx, s.DB, `SELECT id, revision_id, branch_sha, branch_base_sha, base_sha, target_branch, target_sha, merge_sha, merge_objects, message, actor_id, phase FROM revision_publish_claims WHERE revision_id = ?`, revID).Scan(
		&c.ID, &c.RevisionID, &c.BranchSHA, &c.BranchBaseSHA, &c.BaseSHA, &c.TargetBranch, &c.TargetSHA, &c.MergeSHA, &c.Objects, &c.Message, &c.By, &c.Phase)
	if errors.Is(err, sql.ErrNoRows) {
		return c, false, nil
	}
	return c, err == nil, err
}

// createClaim runs under the revision gate, after saving and checking approval.
func (s *Service) createClaim(ctx context.Context, rev revisions.Revision, repo repos.Repo, target, merge, message, by string, objects []byte) (claim, error) {
	if objects == nil {
		objects = []byte{}
	}
	c := claim{ID: ids.New("pub"), RevisionID: rev.ID, BranchSHA: rev.BranchSHA, BranchBaseSHA: rev.BranchBaseSHA, BaseSHA: rev.BaseSHA,
		TargetBranch: repo.TargetBranch, TargetSHA: target, MergeSHA: merge, Message: message, By: by, Phase: "claimed", Objects: objects}
	err := s.DB.InTx(ctx, func(tx *store.Tx) error {
		res, err := store.Exec(ctx, tx, `UPDATE revisions SET state = ? WHERE id = ? AND state = ? AND branch_sha = ? AND review_round = ?`,
			string(revisions.Publishing), rev.ID, string(revisions.Approved), c.BranchSHA, rev.ReviewRound)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return &revisions.ErrConflict{Code: "state_changed", Msg: "The revision changed before publishing."}
		}
		_, err = store.Exec(ctx, tx, `INSERT INTO revision_publish_claims (revision_id, id, branch_sha, branch_base_sha, base_sha, target_branch, target_sha, merge_sha, merge_objects, message, actor_id, phase, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.RevisionID, c.ID, c.BranchSHA, c.BranchBaseSHA, c.BaseSHA, c.TargetBranch, c.TargetSHA, c.MergeSHA, c.Objects, c.Message, c.By, c.Phase, store.Millis(time.Now()))
		if err != nil {
			return err
		}
		return revisions.Record(ctx, tx, rev.ID, revisions.ActorUser, by, "publishing", map[string]any{"claim": c.ID, "sha": c.BranchSHA, "url": rev.ChangeRequestURL})
	})
	if err == nil {
		s.Revisions.Notify(ctx, rev.ID, "publishing")
	}
	return c, err
}

func (s *Service) claimPhase(ctx context.Context, c claim, phase string) error {
	res, err := store.Exec(ctx, s.DB, `UPDATE revision_publish_claims SET phase = ? WHERE revision_id = ? AND id = ?`, phase, c.RevisionID, c.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("publish claim is no longer current")
	}
	return nil
}

// endClaim is a compare-and-set: stale jobs and webhooks cannot change a later claim.
func (s *Service) endClaim(ctx context.Context, c claim, sha, reason string) error {
	ctx, unlock, err := s.Revisions.Gate(ctx, c.RevisionID)
	if err != nil {
		return err
	}
	defer unlock()
	kind, state := "publish_blocked", revisions.Approved
	if sha != "" {
		kind, state = "published", revisions.Published
	}
	err = s.DB.InTx(ctx, func(tx *store.Tx) error {
		res, err := store.Exec(ctx, tx, `UPDATE revisions SET state = ?, published_sha = ?, published_at = ? WHERE id = ? AND state = ? AND branch_sha = ? AND EXISTS (SELECT 1 FROM revision_publish_claims WHERE revision_id = ? AND id = ?)`,
			string(state), sha, publishTime(sha), c.RevisionID, string(revisions.Publishing), c.BranchSHA, c.RevisionID, c.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return errors.New("publish claim is no longer current")
		}
		if _, err := store.Exec(ctx, tx, `DELETE FROM revision_publish_claims WHERE revision_id = ? AND id = ?`, c.RevisionID, c.ID); err != nil {
			return err
		}
		return revisions.Record(ctx, tx, c.RevisionID, revisions.ActorUser, c.By, kind, map[string]any{"sha": sha, "reason": reason, "claim": c.ID})
	})
	if err == nil {
		s.Revisions.Notify(ctx, c.RevisionID, kind)
	}
	return err
}

func publishTime(sha string) any {
	if sha == "" {
		return nil
	}
	return store.Millis(time.Now())
}

// Recover requeues unresolved claims even after the ordinary job retry budget
// was exhausted. Ambiguous network outcomes remain read-only until reconciled.
func (s *Service) Recover(ctx context.Context) error {
	rows, err := store.Query(ctx, s.DB, `SELECT revision_id, actor_id FROM revision_publish_claims`)
	if err != nil {
		return err
	}
	var pending []jobInput
	for rows.Next() {
		var in jobInput
		if err := rows.Scan(&in.RevisionID, &in.By); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, in)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, in := range pending {
		if _, err := s.Jobs.Enqueue(ctx, s.DB, JobPublish, in, jobs.EnqueueOptions{Key: JobPublish + ":" + in.RevisionID, MaxAttempts: 3}); err != nil {
			return err
		}
	}
	return nil
}
