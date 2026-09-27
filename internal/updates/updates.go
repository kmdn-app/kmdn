// Package updates brings new Published commits into open revisions: kmdn's
// rebase, prepared automatically, previewed, then applied by a person
// (docs/specs/06-git-and-forges.md#updates-from-published).
package updates

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/kmdn-app/kmdn/internal/docengine"
	"github.com/kmdn-app/kmdn/internal/gitmirror"
	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/jobs"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
)

// JobPrepare prepares a revision's pending update.
const JobPrepare = "revision.prepare_update"

// File kinds in an update.
const (
	KindMerge           = "merge"
	KindDeletedUpstream = "deleted_upstream" // edited here, deleted on Published
	KindDeletedBoth     = "deleted_both"
)

// Docs is what applying needs from the collaborative documents.
type Docs interface {
	FlushRevision(ctx context.Context, revID string)
	ApplyDoc(ctx context.Context, repo repos.Repo, rev revisions.Revision, c revisions.Caller, p string, doc json.RawMessage, kind string) error
	// SaveBeforeUpdate commits unsaved work (Save all) before applying.
	SaveBeforeUpdate(ctx context.Context, repo repos.Repo, rev revisions.Revision, c revisions.Caller) error
}

// Service prepares and applies updates.
type Service struct {
	DB        *store.DB
	Repos     *repos.Service
	Revisions *revisions.Service
	Engine    *docengine.Engine
	Docs      Docs
	Jobs      *jobs.Queue
	// Publish sends live events (realtime.Hub.Publish). Optional.
	Publish func(scope string, event map[string]any)
	Log     *slog.Logger
}

// Update is a prepared merge of Published into a revision.
type Update struct {
	ID         string    `json:"id"`
	RevisionID string    `json:"revision_id"`
	FromSHA    string    `json:"from_sha"`
	TargetSHA  string    `json:"target_sha"`
	State      string    `json:"state"`
	PreparedAt time.Time `json:"prepared_at"`
	Files      []File    `json:"files"`
}

// File is one page of an update.
type File struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	Conflicts int    `json:"conflicts"`
	BaseMD    string `json:"-"`
	TheirsMD  string `json:"-"`
}

// Register wires the prepare job and the head-changed hook.
func (s *Service) Register() {
	s.Jobs.Register(JobPrepare, func(ctx context.Context, j jobs.Job) (any, error) {
		var in struct {
			RevisionID string `json:"revision_id"`
		}
		if err := j.Decode(&in); err != nil {
			return nil, jobs.Permanent(err)
		}
		return nil, s.Prepare(ctx, in.RevisionID)
	})
	s.Repos.OnHeadChanged = append(s.Repos.OnHeadChanged, s.headChanged)
	s.Revisions.PendingUpdates = func(ctx context.Context, rev revisions.Revision) (bool, error) {
		u, err := Pending(ctx, s.DB, rev.ID)
		return u != nil, err
	}
}

var openStates = []revisions.State{revisions.Editing, revisions.InReview, revisions.Approved}

// headChanged looks at every open revision when Published moves: those it
// doesn't touch fast-forward, the others get an update prepared.
func (s *Service) headChanged(ctx context.Context, repo repos.Repo, _, to string) error {
	list, err := revisions.List(ctx, s.DB, repo.ID, revisions.Filter{States: openStates, Limit: 200})
	if err != nil {
		return err
	}
	for _, rev := range list {
		if rev.BaseSHA == "" || rev.BaseSHA == to {
			continue
		}
		touched, err := s.touched(ctx, repo, rev, to)
		if err != nil {
			s.Log.Error("updates: changed paths", "revision", rev.ID, "err", err)
			continue
		}
		if len(touched) == 0 {
			if _, err := store.Exec(ctx, s.DB, `UPDATE revisions SET base_sha = ? WHERE id = ? AND base_sha = ?`, to, rev.ID, rev.BaseSHA); err != nil {
				return err
			}
			continue
		}
		if _, err := s.Jobs.Enqueue(ctx, s.DB, JobPrepare, map[string]string{"revision_id": rev.ID}, jobs.EnqueueOptions{Key: JobPrepare + ":" + rev.ID}); err != nil {
			return err
		}
	}
	return nil
}

// touched returns the revision's files Published changed between the
// revision's base and to, keyed by the revision's path, with the path they
// have on Published (a renamed page's old path).
func (s *Service) touched(ctx context.Context, repo repos.Repo, rev revisions.Revision, to string) (map[string]revisions.File, error) {
	changed, err := s.Repos.Mirror(repo).ChangedPaths(ctx, rev.BaseSHA, to)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, p := range changed {
		set[p] = true
	}
	files, err := revisions.Files(ctx, s.DB, rev.ID)
	if err != nil {
		return nil, err
	}
	out := map[string]revisions.File{}
	for _, f := range files {
		if set[upstreamPath(f)] {
			out[f.Path] = f
		}
	}
	return out, nil
}

// upstreamPath is where the revision's file lives on Published.
func upstreamPath(f revisions.File) string {
	if f.FromPath != "" {
		return f.FromPath
	}
	return f.Path
}

// Prepare computes the revision's pending update against the current
// Published head, replacing any older one. Nothing touches the documents.
func (s *Service) Prepare(ctx context.Context, revID string) error {
	rev, err := revisions.Get(ctx, s.DB, revID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return jobs.Permanent(err)
		}
		return err
	}
	if !rev.State.Open() || rev.State == revisions.Publishing {
		return nil
	}
	repo, err := repos.Get(ctx, s.DB, rev.RepoID)
	if err != nil {
		return err
	}
	head := repo.HeadSHA
	if head == "" || head == rev.BaseSHA {
		return nil
	}
	touched, err := s.touched(ctx, repo, rev, head)
	if err != nil {
		return err
	}
	if len(touched) == 0 {
		_, err := store.Exec(ctx, s.DB, `UPDATE revisions SET base_sha = ? WHERE id = ? AND base_sha = ?`, head, rev.ID, rev.BaseSHA)
		return err
	}
	s.Docs.FlushRevision(ctx, rev.ID)
	files, err := revisions.Files(ctx, s.DB, rev.ID)
	if err != nil {
		return err
	}
	var out []File
	for _, f := range files {
		if _, ok := touched[f.Path]; !ok {
			continue
		}
		pf, err := s.prepareFile(ctx, repo, f, head)
		if err != nil {
			return err
		}
		out = append(out, pf)
	}
	u := Update{ID: ids.New("upd"), RevisionID: rev.ID, FromSHA: rev.BaseSHA, TargetSHA: head, State: "pending", PreparedAt: time.Now()}
	err = s.DB.InTx(ctx, func(tx *store.Tx) error {
		if _, err := store.Exec(ctx, tx, `DELETE FROM revision_updates WHERE revision_id = ? AND state = 'pending'`, rev.ID); err != nil {
			return err
		}
		if _, err := store.Exec(ctx, tx, `INSERT INTO revision_updates (id, revision_id, from_sha, target_sha, state, prepared_at) VALUES (?, ?, ?, ?, 'pending', ?)`,
			u.ID, rev.ID, u.FromSHA, u.TargetSHA, store.Millis(u.PreparedAt)); err != nil {
			return err
		}
		conflicts := 0
		for _, f := range out {
			conflicts += f.Conflicts
			if _, err := store.Exec(ctx, tx, `INSERT INTO revision_update_files (update_id, path, kind, base_md, theirs_md, conflicts) VALUES (?, ?, ?, ?, ?, ?)`,
				u.ID, f.Path, f.Kind, f.BaseMD, f.TheirsMD, f.Conflicts); err != nil {
				return err
			}
		}
		return revisions.Record(ctx, tx, rev.ID, revisions.ActorSystem, "", "update_available", map[string]any{"target": head, "files": len(out), "conflicts": conflicts})
	})
	if err != nil {
		return err
	}
	s.emit(rev.ID, "prepared")
	return nil
}

func (s *Service) prepareFile(ctx context.Context, repo repos.Repo, f revisions.File, head string) (File, error) {
	pf := File{Path: f.Path, BaseMD: f.BaseMD}
	theirs, err := s.Repos.ReadFile(ctx, repo, upstreamPath(f), head)
	gone := errors.Is(err, gitmirror.ErrNotFound)
	if err != nil && !gone {
		return pf, err
	}
	switch {
	case gone && f.Op == revisions.OpDelete:
		pf.Kind = KindDeletedBoth
	case gone:
		pf.Kind = KindDeletedUpstream
		pf.Conflicts = 1
	default:
		pf.Kind = KindMerge
		pf.TheirsMD = theirs.Content
		if f.Op == revisions.OpDelete {
			// Deleting a page Published just changed: the change would be lost.
			pf.Kind = KindDeletedUpstream
			pf.Conflicts = 1
			break
		}
		base := f.BaseMD
		if f.Op == revisions.OpAdd {
			base = "" // created on both sides
		}
		m, err := s.Engine.Merge3(ctx, base, f.ContentMD, theirs.Content)
		if err != nil {
			return pf, err
		}
		pf.Conflicts = m.Conflicts
	}
	return pf, nil
}

func (s *Service) emit(revID, action string) {
	if s.Publish != nil {
		s.Publish("revision:"+revID, map[string]any{"type": "update", "action": action, "revision": revID})
	}
}

// Pending returns the revision's pending update (nil when there is none).
func Pending(ctx context.Context, q store.Querier, revID string) (*Update, error) {
	var u Update
	var at int64
	err := store.QueryRow(ctx, q, `SELECT id, revision_id, from_sha, target_sha, state, prepared_at FROM revision_updates WHERE revision_id = ? AND state = 'pending' ORDER BY prepared_at DESC LIMIT 1`, revID).
		Scan(&u.ID, &u.RevisionID, &u.FromSHA, &u.TargetSHA, &u.State, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.PreparedAt = store.FromMillis(at)
	rows, err := store.Query(ctx, q, `SELECT path, kind, base_md, theirs_md, conflicts FROM revision_update_files WHERE update_id = ? ORDER BY path`, u.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var f File
		if err := rows.Scan(&f.Path, &f.Kind, &f.BaseMD, &f.TheirsMD, &f.Conflicts); err != nil {
			return nil, err
		}
		u.Files = append(u.Files, f)
	}
	return &u, rows.Err()
}

// Result is what applying did.
type Result struct {
	Files     int  `json:"files"`
	Conflicts int  `json:"conflicts"`
	Reopened  bool `json:"reopened"` // sent back to Editing because of conflicts
}

// Apply merges the pending update into the revision's pages on c's behalf:
// a checkpoint first, then each page re-merged against its current content
// and written as one change by a "sync" client (never credited). The base
// moves to the update's target.
func (s *Service) Apply(ctx context.Context, rev revisions.Revision, c revisions.Caller, updateID string) (Result, error) {
	var res Result
	acc, err := s.Revisions.AccessFor(ctx, rev, c)
	if err != nil {
		return res, err
	}
	// Editors while Editing, assigned reviewers In review: whoever can edit.
	if !acc.CanEdit {
		return res, revisions.ErrForbidden
	}
	u, err := Pending(ctx, s.DB, rev.ID)
	if err != nil {
		return res, err
	}
	if u == nil || u.ID != updateID {
		return res, &revisions.ErrConflict{Code: "update_stale", Msg: "Published changed again. Review the new update."}
	}
	repo, err := repos.Get(ctx, s.DB, rev.RepoID)
	if err != nil {
		return res, err
	}
	if err := s.Docs.SaveBeforeUpdate(ctx, repo, rev, c); err != nil {
		return res, err
	}
	s.Docs.FlushRevision(ctx, rev.ID)
	files, err := revisions.Files(ctx, s.DB, rev.ID)
	if err != nil {
		return res, err
	}
	byPath := map[string]revisions.File{}
	for _, f := range files {
		byPath[f.Path] = f
	}
	for _, uf := range u.Files {
		f, ok := byPath[uf.Path]
		if !ok {
			continue // removed from the revision since
		}
		res.Files++
		switch uf.Kind {
		case KindDeletedBoth:
			if err := s.Revisions.DropFile(ctx, rev, uf.Path); err != nil {
				return res, err
			}
		case KindDeletedUpstream:
			res.Conflicts++
			if _, err := store.Exec(ctx, s.DB, `UPDATE revision_files SET has_conflicts = TRUE, conflict = ?, base_md = '' WHERE revision_id = ? AND path = ?`, KindDeletedUpstream, rev.ID, uf.Path); err != nil {
				return res, err
			}
		default:
			base := uf.BaseMD
			if f.Op == revisions.OpAdd {
				base = ""
			}
			// Collaborators may have edited since it was prepared: merge again.
			m, err := s.Engine.Merge3(ctx, base, f.ContentMD, uf.TheirsMD)
			if err != nil {
				return res, err
			}
			if err := s.Docs.ApplyDoc(ctx, repo, rev, c, uf.Path, m.Doc, "sync"); err != nil {
				return res, err
			}
			res.Conflicts += m.Conflicts
			if _, err := store.Exec(ctx, s.DB, `UPDATE revision_files SET base_md = ?, has_conflicts = ?, conflict = '' WHERE revision_id = ? AND path = ?`, uf.TheirsMD, m.Conflicts > 0, rev.ID, uf.Path); err != nil {
				return res, err
			}
		}
	}
	err = s.DB.InTx(ctx, func(tx *store.Tx) error {
		now := store.Millis(time.Now())
		if _, err := store.Exec(ctx, tx, `UPDATE revision_updates SET state = 'applied', applied_at = ?, applied_by = ? WHERE id = ?`, now, c.User.ID, u.ID); err != nil {
			return err
		}
		if _, err := store.Exec(ctx, tx, `UPDATE revisions SET base_sha = ?, has_conflicts = ? WHERE id = ?`, u.TargetSHA, res.Conflicts > 0, rev.ID); err != nil {
			return err
		}
		if res.Conflicts > 0 && (rev.State == revisions.InReview || rev.State == revisions.Approved) {
			if _, err := store.Exec(ctx, tx, `UPDATE revisions SET state = ? WHERE id = ?`, string(revisions.Editing), rev.ID); err != nil {
				return err
			}
			if _, err := store.Exec(ctx, tx, `UPDATE approvals SET dismissed_at = ? WHERE revision_id = ? AND review_round = ? AND dismissed_at IS NULL`, now, rev.ID, rev.ReviewRound); err != nil {
				return err
			}
			res.Reopened = true
		}
		return revisions.Record(ctx, tx, rev.ID, revisions.ActorUser, c.User.ID, "updates_applied", map[string]any{"files": res.Files, "conflicts": res.Conflicts, "target": u.TargetSHA})
	})
	if err != nil {
		return res, err
	}
	s.emit(rev.ID, "applied")
	s.Revisions.Notify(ctx, rev.ID, "updates_applied")
	return res, nil
}

// ResolvePage settles a page-level conflict: Published deleted a page the
// revision edits. "keep" keeps the revision's version (publishing adds the
// page back); "delete" accepts the deletion and drops the page from the revision.
func (s *Service) ResolvePage(ctx context.Context, rev revisions.Revision, c revisions.Caller, p, choice string) error {
	acc, err := s.Revisions.AccessFor(ctx, rev, c)
	if err != nil {
		return err
	}
	if !acc.CanEdit {
		return revisions.ErrForbidden
	}
	f, err := revisions.FileAt(ctx, s.DB, rev.ID, p)
	if err != nil {
		return err
	}
	if f.Conflict != KindDeletedUpstream {
		return &revisions.ErrConflict{Code: "no_conflict", Msg: "This page has no conflict to resolve."}
	}
	switch choice {
	case "keep":
		op := revisions.OpAdd
		if f.Op == revisions.OpDelete {
			// The revision deleted it too, and Published changed it: keep the deletion out.
			return s.Revisions.DropFile(ctx, rev, p)
		}
		if _, err := store.Exec(ctx, s.DB, `UPDATE revision_files SET op = ?, from_path = '', base_md = '', conflict = '', has_conflicts = FALSE WHERE id = ?`, op, f.ID); err != nil {
			return err
		}
	case "delete":
		if err := s.Revisions.DropFile(ctx, rev, p); err != nil {
			return err
		}
	default:
		return errors.New("updates: choice is keep or delete")
	}
	_ = revisions.Record(ctx, s.DB, rev.ID, revisions.ActorUser, c.User.ID, "conflict_resolved", map[string]any{"path": p, "choice": choice})
	if err := s.Revisions.SettleConflicts(ctx, rev.ID, p); err != nil {
		return err
	}
	s.Revisions.Notify(ctx, rev.ID, "conflict_resolved")
	return nil
}
