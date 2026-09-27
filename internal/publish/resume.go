package publish

import (
	"context"
	"errors"
	"strings"

	"github.com/kmdn-app/kmdn/internal/audit"
	"github.com/kmdn-app/kmdn/internal/branches"
	"github.com/kmdn-app/kmdn/internal/forge"
	"github.com/kmdn-app/kmdn/internal/gitmirror"
	"github.com/kmdn-app/kmdn/internal/jobs"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
)

// resumeClaim never saves or reads a mutable manifest. Only the recorded
// candidate can finish this claim, including after a process restart.
func (s *Service) resumeClaim(ctx context.Context, repo repos.Repo, rev revisions.Revision, c claim, adapter forge.Adapter, cred *gitmirror.Credential, hint ...string) (string, string, error) {
	// A webhook and a retried job may race. Only one external attempt may
	// decide whether a refusal proves that this claim caused no merge.
	candidate := make(chan struct{}, 1)
	value, _ := s.claimRuns.LoadOrStore(rev.ID, candidate)
	gate := value.(chan struct{})
	select {
	case gate <- struct{}{}:
	case <-ctx.Done():
		return "", "", ctx.Err()
	}
	defer func() { <-gate }()
	var err error
	if rev, err = revisions.Get(ctx, s.DB, rev.ID); err != nil {
		return "", "", err
	}
	if rev.State == revisions.Published {
		return rev.PublishedSHA, "", nil
	}
	current, exists, err := s.loadClaim(ctx, rev.ID)
	if err != nil {
		return "", "", err
	}
	if !exists || current.ID != c.ID {
		return "", "", errors.New("publish claim is no longer current")
	}
	c = current
	if rev.BranchSHA != c.BranchSHA {
		return "", "", errors.New("publish claim no longer matches the revision's saved branch")
	}
	repo.TargetBranch = c.TargetBranch
	if len(hint) > 0 && hint[0] != "" {
		return hint[0], "", s.finishClaim(ctx, repo, rev, c, hint[0])
	}
	m := s.Repos.Mirror(repo)
	if err := m.Init(ctx, cred); err != nil {
		return "", "", err
	}
	head, err := m.Head(ctx)
	if err != nil {
		return "", "", err
	}
	if c.MergeSHA != "" {
		if !m.HasCommit(ctx, c.MergeSHA) {
			// Backups omit mirror caches. Restore the exact prepared object,
			// whose parents were already pushed before this claim was recorded.
			if !m.HasCommit(ctx, c.BranchSHA) {
				if _, err := m.FetchBranch(ctx, cred, rev.Branch, branches.LocalRef(rev)); err != nil {
					return "", "", err
				}
			}
			if err := m.RestoreCommitPack(ctx, cred, c.MergeSHA, c.Objects); err != nil {
				return "", "", err
			}
			if err := m.KeepRef(ctx, "refs/kmdn/publishing/"+rev.ID, c.MergeSHA); err != nil {
				return "", "", err
			}
		}
		landed, err := m.IsAncestor(ctx, c.MergeSHA, head)
		if err != nil {
			return "", "", err
		}
		if landed {
			return c.MergeSHA, "", s.finishClaim(ctx, repo, rev, c, c.MergeSHA)
		}
		first := c.Phase == "claimed"
		if err := s.claimPhase(ctx, c, "merging"); err != nil {
			return "", "", err
		}
		if err := m.Push(ctx, cred, c.MergeSHA, c.TargetBranch, c.TargetSHA); err != nil {
			if first && errors.Is(err, gitmirror.ErrStale) {
				if endErr := s.endClaim(ctx, c, "", "published_moved"); endErr != nil {
					return "", "", endErr
				}
				return "", "", jobs.Permanent(err)
			}
			return "", "", err
		}
		return c.MergeSHA, "", s.finishClaim(ctx, repo, rev, c, c.MergeSHA)
	}
	cr, ok := adapter.(forge.ChangeRequester)
	if !ok {
		return "", "", errors.New("the claimed forge merge is unavailable")
	}
	// A trailer is only a candidate. Completion separately checks the exact
	// source parent and that the commit is reachable from the target.
	trailer := "Kmdn-Revision: " + s.Branches.RevisionURL(repo, rev)
	if sha, found, err := m.FindTrailer(ctx, c.BaseSHA, head, trailer); err != nil {
		return "", "", err
	} else if found {
		if err := s.verifyClaimMerge(ctx, repo, c, sha); err == nil {
			return sha, "", s.finishClaim(ctx, repo, rev, c, sha)
		}
	}
	if c.Phase != "claimed" {
		if reader, ok := adapter.(forge.ChangeRequestStatusReader); ok {
			status, err := reader.ChangeRequestStatus(ctx, repo.ForgeRepo(), rev.ChangeRequestRef)
			if err != nil {
				return "", "", err
			}
			if status.Merged {
				if status.HeadSHA != c.BranchSHA {
					return "", "", errors.New("the forge merged a different source commit; publishing remains read-only")
				}
				return status.MergeSHA, "", s.finishClaim(ctx, repo, rev, c, status.MergeSHA)
			}
			if status.Closed {
				return "", "", s.endClaim(ctx, c, "", "change_request_closed")
			}
			if !status.Open {
				return "", "", errors.New("the forge merge outcome is not yet known")
			}
			if status.HeadSHA != c.BranchSHA {
				return "", "", errors.New("the claimed source branch changed on the forge")
			}
			if status.Queued {
				return "", rev.ChangeRequestURL, nil
			}
		} else if c.Phase == "queued" {
			return "", rev.ChangeRequestURL, nil
		}
	}
	first := c.Phase == "claimed"
	if err := s.Branches.MarkReady(ctx, repo, rev); err != nil {
		return "", "", err
	}
	if err := s.claimPhase(ctx, c, "merging"); err != nil {
		return "", "", err
	}
	title, body, _ := strings.Cut(c.Message, "\n")
	ref := forge.ChangeRequest{URL: rev.ChangeRequestURL, Ref: rev.ChangeRequestRef, Node: rev.ChangeRequestNode}
	res, err := cr.MergeChangeRequest(ctx, repo.ForgeRepo(), ref, forge.MergeInput{Title: title, Body: strings.TrimSpace(body), HeadSHA: c.BranchSHA})
	if err != nil {
		// A first explicit refusal proves this attempt caused no merge. An
		// earlier timeout cannot be cleared by a later refusal.
		if first {
			for code, known := range map[string]error{"merge_commits_disabled": forge.ErrMergeCommitsDisabled, "approvals_required": forge.ErrApprovalsRequired, "not_mergeable": forge.ErrNotMergeable, "head_changed": forge.ErrHeadChanged} {
				if errors.Is(err, known) {
					if endErr := s.endClaim(ctx, c, "", code); endErr != nil {
						return "", "", endErr
					}
					return "", "", jobs.Permanent(err)
				}
			}
		}
		return "", "", err
	}
	if res.Queued {
		if err := s.claimPhase(ctx, c, "queued"); err != nil {
			return "", "", err
		}
		return "", rev.ChangeRequestURL, nil
	}
	return res.SHA, "", s.finishClaim(ctx, repo, rev, c, res.SHA)
}

func (s *Service) verifyClaimMerge(ctx context.Context, repo repos.Repo, c claim, sha string) error {
	if sha == "" {
		return errors.New("the forge has not confirmed a merge commit")
	}
	_, cred, err := s.Branches.Forge(ctx, repo)
	if err != nil {
		return err
	}
	m := s.Repos.Mirror(repo)
	if err := m.Init(ctx, cred); err != nil {
		return err
	}
	head, err := m.Head(ctx)
	if err != nil {
		return err
	}
	landed, err := m.IsAncestor(ctx, sha, head)
	if err != nil {
		return err
	}
	if !landed {
		return errors.New("the claimed merge is not on the target branch")
	}
	log, err := m.Log(ctx, sha, "", 1)
	if err != nil {
		return err
	}
	if len(log) != 1 || log[0].SHA != sha || len(log[0].Parents) != 2 || log[0].Parents[1] != c.BranchSHA {
		return errors.New("the merge does not contain the exact claimed source commit")
	}
	if c.MergeSHA != "" && c.MergeSHA != sha {
		return errors.New("the merge differs from the prepared commit")
	}
	return nil
}

func (s *Service) finishClaim(ctx context.Context, repo repos.Repo, rev revisions.Revision, c claim, sha string) error {
	repo.TargetBranch = c.TargetBranch
	if err := s.verifyClaimMerge(ctx, repo, c, sha); err != nil {
		return err
	}
	// Sync calls hooks for other revisions. No revision/branch gate may be
	// held here, otherwise simultaneous publishes can acquire gates in a cycle.
	if err := s.Repos.Sync(ctx, repo.ID); err != nil {
		if _, err := s.Repos.EnqueueSync(ctx, repo.ID); err != nil {
			s.Log.Warn("queue sync after publish", "err", err)
		}
	}
	if err := s.endClaim(ctx, c, sha, ""); err != nil {
		return err
	}
	_ = audit.Write(ctx, s.DB, audit.Entry{ActorType: "user", ActorID: c.By, Action: "revision.published", TargetType: "revision", TargetID: rev.ID, RepoID: repo.ID, Data: map[string]any{"sha": sha, "claim": c.ID}})
	s.deleteBranch(ctx, repo, rev.Branch)
	return nil
}
