package collab

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kmdn-app/kmdn/internal/branches"
	"github.com/kmdn-app/kmdn/internal/gitmirror"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

type savedFile struct {
	Path      string `json:"path"`
	Op        string `json:"op"`
	FromPath  string `json:"from_path,omitempty"`
	Content   string `json:"content"`
	State     []byte `json:"state,omitempty"`
	SourceMap string `json:"source_map,omitempty"`
}

type savePayload struct {
	Files  []savedFile        `json:"files"`
	Assets []revisions.Asset  `json:"assets"`
	Author gitmirror.Identity `json:"author"`
}

type saveIntent struct {
	Checkpoint CheckpointView
	Branch     string
	PriorSHA   string
	BaseSHA    string
	Hash       string
	Objects    []byte
	Payload    savePayload
}

// captureSave pairs each captured document with the exact markdown used in
// Git. In particular, a failed background materialization must not save stale
// markdown while claiming the newer document was saved.
func (h *Hub) captureSave(ctx context.Context, revID string) ([]revisions.File, savePayload, error) {
	files, err := revisions.Files(ctx, h.DB, revID)
	if err != nil {
		return nil, savePayload{}, err
	}
	assets, err := revisions.Assets(ctx, h.DB, revID)
	if err != nil {
		return nil, savePayload{}, err
	}
	payload := savePayload{Assets: assets, Files: make([]savedFile, 0, len(files))}
	for _, f := range files {
		captured := savedFile{Path: f.Path, Op: f.Op, FromPath: f.FromPath, Content: f.ContentMD}
		if f.Op != revisions.OpDelete {
			docID, state, err := h.stateOf(ctx, revID, f.Path)
			if err != nil {
				return nil, savePayload{}, err
			}
			if state != nil {
				if err := store.QueryRow(ctx, h.DB, `SELECT source_map FROM ydocs WHERE id = ?`, docID).Scan(&captured.SourceMap); err != nil {
					return nil, savePayload{}, err
				}
				md, err := h.Engine.YMaterialize(ctx, state, captured.SourceMap)
				if err != nil {
					return nil, savePayload{}, err
				}
				if md != f.ContentMD {
					return nil, savePayload{}, fmt.Errorf("save %s: document has not materialized; retry the save", f.Path)
				}
				captured.State = state
			}
		}
		payload.Files = append(payload.Files, captured)
	}
	return files, payload, nil
}

func (h *Hub) putSaveIntent(ctx context.Context, revID string, in saveIntent) error {
	payload, err := json.Marshal(in.Payload)
	if err != nil {
		return err
	}
	var creator any
	if in.Checkpoint.CreatedBy != "" {
		creator = in.Checkpoint.CreatedBy
	}
	_, err = store.Exec(ctx, h.DB, `INSERT INTO revision_save_intents
(revision_id, checkpoint_id, branch, commit_sha, prior_branch_sha, base_sha, content_hash, created_by, created_at, name, commit_objects, payload)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, revID, in.Checkpoint.ID, in.Branch, in.Checkpoint.CommitSHA, in.PriorSHA, in.BaseSHA, in.Hash, creator, store.Millis(in.Checkpoint.CreatedAt), in.Checkpoint.Name, in.Objects, payload)
	return err
}

// RecoverSaveLocked completes the exact interrupted save, if any. Callers hold
// the revision gate and branch lock. It never includes newer live edits.
func (h *Hub) RecoverSaveLocked(ctx context.Context, repo repos.Repo, rev revisions.Revision) (CheckpointView, bool, error) {
	var in saveIntent
	var payload []byte
	var created int64
	err := store.QueryRow(ctx, h.DB, `SELECT checkpoint_id, branch, commit_sha, prior_branch_sha, base_sha, content_hash, COALESCE(created_by, ''), created_at, name, commit_objects, payload FROM revision_save_intents WHERE revision_id = ?`, rev.ID).
		Scan(&in.Checkpoint.ID, &in.Branch, &in.Checkpoint.CommitSHA, &in.PriorSHA, &in.BaseSHA, &in.Hash, &in.Checkpoint.CreatedBy, &created, &in.Checkpoint.Name, &in.Objects, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return CheckpointView{}, false, nil
	}
	if err != nil {
		return CheckpointView{}, false, err
	}
	if err := json.Unmarshal(payload, &in.Payload); err != nil {
		return CheckpointView{}, true, fmt.Errorf("read prepared save: %w", err)
	}
	in.Checkpoint.CreatedAt = store.FromMillis(created)
	in.Checkpoint.CreatedByName = in.Payload.Author.Name
	in.Checkpoint.Kind = CheckpointSave
	in.Checkpoint.Files = len(in.Payload.Files)
	current, err := revisions.Get(ctx, h.DB, rev.ID)
	if err != nil {
		return CheckpointView{}, true, err
	}
	if current.Branch != in.Branch || current.BranchSHA != in.PriorSHA {
		return CheckpointView{}, true, branches.ErrBranchMoved
	}
	if err := h.Branches.PushSave(ctx, repo, current, in.Checkpoint.CommitSHA, in.PriorSHA, in.Objects); err != nil {
		return CheckpointView{}, true, err
	}
	if err := h.finishSave(ctx, current, in); err != nil {
		return CheckpointView{}, true, err
	}
	h.Branches.FinishSave(ctx, repo, rev.ID)
	h.Revisions.Notify(ctx, rev.ID, "saved")
	return in.Checkpoint, true, nil
}

func (h *Hub) finishSave(ctx context.Context, rev revisions.Revision, in saveIntent) error {
	cp := in.Checkpoint
	return h.DB.InTx(ctx, func(tx *store.Tx) error {
		var creator any
		if cp.CreatedBy != "" {
			creator = cp.CreatedBy
		}
		if _, err := store.Exec(ctx, tx, `INSERT INTO revision_checkpoints (id, revision_id, name, kind, created_by, created_at, commit_sha, content_hash) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			cp.ID, rev.ID, cp.Name, CheckpointSave, creator, store.Millis(cp.CreatedAt), cp.CommitSHA, in.Hash); err != nil {
			return err
		}
		for _, f := range in.Payload.Files {
			if _, err := store.Exec(ctx, tx, `INSERT INTO revision_checkpoint_files (checkpoint_id, path, op, from_path, content_md, ydoc_state) VALUES (?, ?, ?, ?, ?, ?)`,
				cp.ID, f.Path, f.Op, f.FromPath, f.Content, f.State); err != nil {
				return err
			}
		}
		res, err := store.Exec(ctx, tx, `UPDATE revisions SET branch_sha = ?, branch_base_sha = ? WHERE id = ? AND branch = ? AND branch_sha = ?`, cp.CommitSHA, in.BaseSHA, rev.ID, in.Branch, in.PriorSHA)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return branches.ErrBranchMoved
		}
		if err := revisions.Record(ctx, tx, rev.ID, revisions.ActorUser, cp.CreatedBy, "saved", map[string]any{"checkpoint": cp.ID, "sha": cp.CommitSHA, "message": cp.Name}); err != nil {
			return err
		}
		_, err = store.Exec(ctx, tx, `DELETE FROM revision_save_intents WHERE revision_id = ? AND commit_sha = ?`, rev.ID, cp.CommitSHA)
		return err
	})
}

// SaveCurrentLocked recovers an earlier attempt, then saves current content for
// an action such as submit or publish. Callers hold the gate and branch lock.
func (h *Hub) SaveCurrentLocked(ctx context.Context, repo repos.Repo, rev revisions.Revision, by users.User, message string) error {
	if _, _, err := h.RecoverSaveLocked(ctx, repo, rev); err != nil {
		return err
	}
	_, err := h.SaveLocked(ctx, repo, rev, by, message)
	if errors.Is(err, ErrNothingToSave) {
		return nil
	}
	return err
}
