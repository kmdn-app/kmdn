package collab

import (
	"context"
	"database/sql"
	"errors"
	"math/rand/v2"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/realtime"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
)

// Checkpoint kinds (docs/specs/05-collaboration.md#saving-and-checkpoints).
// Checkpoints are the commits Save all writes; the other kinds are from
// before that and only appear in older revisions.
const (
	CheckpointSave = "save"

	CheckpointAuto       = "auto"
	CheckpointNamed      = "named"
	CheckpointSubmit     = "submit"
	CheckpointPreUpdate  = "pre_update"
	CheckpointPreRestore = "pre_restore"
)

// Apply turns a page into markdown as a new change written on c's behalf
// (restore now, the assistant later): the room's document is diffed block by
// block, so concurrent edits elsewhere survive. kind labels the writer's
// Yjs client (restore, assistant).
func (h *Hub) Apply(ctx context.Context, repo repos.Repo, rev revisions.Revision, c revisions.Caller, p, markdown, kind string) error {
	h.init()
	room, err := h.room(ctx, repo, rev, p, true, c.User.ID)
	if err != nil {
		return err
	}
	state := room.merged(ctx)
	if state == nil {
		return errors.New("collab: document unavailable")
	}
	client := uint64(rand.Uint32())
	update, err := h.Engine.YApplyMarkdown(ctx, state, markdown, uint32(client))
	if err != nil {
		return err
	}
	if len(update) <= 2 {
		return nil // already identical
	}
	return room.ingest(ctx, update, []uint64{client}, c, nil, kind)
}

// PutAnchor stores a comment thread's position in the page's document (its
// "comments" map), on the commenter's behalf, so it moves with the text. Pages
// without a document (nobody edited them yet) keep the quote-only anchor.
func (h *Hub) PutAnchor(ctx context.Context, repo repos.Repo, rev revisions.Revision, c revisions.Caller, p, threadID string, anchorJSON []byte) error {
	h.init()
	room, err := h.room(ctx, repo, rev, p, false, c.User.ID)
	if err != nil {
		var je *realtime.JoinError
		if errors.As(err, &je) && je.Code == "no_document" {
			return nil
		}
		return err
	}
	state := room.merged(ctx)
	if state == nil {
		return errors.New("collab: document unavailable")
	}
	client := uint64(rand.Uint32())
	update, err := h.Engine.YSetMapEntry(ctx, state, uint32(client), "comments", threadID, string(anchorJSON))
	if err != nil {
		return err
	}
	return room.ingest(ctx, update, []uint64{client}, c, nil, "comment")
}

func (h *Hub) liveRooms(revID string) []*Room {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []*Room
	for _, r := range h.rooms {
		if r.revID != revID {
			continue
		}
		select {
		case <-r.ready:
			if r.loadErr == nil {
				out = append(out, r)
			}
		default:
		}
	}
	return out
}

// stateOf returns a page document's current state: from its room when loaded,
// otherwise from the store.
func (h *Hub) stateOf(ctx context.Context, revID, p string) (docID string, state []byte, err error) {
	h.mu.Lock()
	r := h.rooms[key(revID, p)]
	h.mu.Unlock()
	if r != nil {
		select {
		case <-r.ready:
			if r.loadErr == nil {
				if s := r.merged(ctx); s != nil {
					return r.docID, s, nil
				}
			}
		default:
		}
	}
	err = store.QueryRow(ctx, h.DB, `SELECT id FROM ydocs WHERE revision_id = ? AND path = ?`, revID, p).Scan(&docID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, nil
	} else if err != nil {
		return "", nil, err
	}
	var snap []byte
	var seq int64
	if err := store.QueryRow(ctx, h.DB, `SELECT state, update_seq FROM ydoc_snapshots WHERE ydoc_id = ? ORDER BY update_seq DESC LIMIT 1`, docID).Scan(&snap, &seq); err != nil {
		return "", nil, err
	}
	all := [][]byte{snap}
	rows, err := store.Query(ctx, h.DB, `SELECT data FROM ydoc_updates WHERE ydoc_id = ? AND seq > ? ORDER BY seq`, docID, seq)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u []byte
		if err := rows.Scan(&u); err != nil {
			return "", nil, err
		}
		all = append(all, u)
	}
	if err := rows.Err(); err != nil {
		return "", nil, err
	}
	state, err = h.Engine.YMerge(ctx, all)
	return docID, state, err
}

// FlushRevision persists and materializes the revision's open rooms, so
// revision_files has every edit (before checkpoints and publishing).
func (h *Hub) FlushRevision(ctx context.Context, revID string) {
	h.init()
	for _, r := range h.liveRooms(revID) {
		r.flush(ctx)
		r.materialize(ctx)
	}
}

// StateOf returns a page document's current state (nil when it has none).
func (h *Hub) StateOf(ctx context.Context, revID, p string) (docID string, state []byte, err error) {
	h.init()
	return h.stateOf(ctx, revID, p)
}

// CheckpointView is a checkpoint in listings.
type CheckpointView struct {
	ID            string    `json:"id"`
	Name          string    `json:"name,omitempty"`
	Kind          string    `json:"kind"`
	CreatedBy     string    `json:"created_by,omitempty"`
	CreatedByName string    `json:"created_by_name,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	Files         int       `json:"file_count"`
	// CommitSHA is the commit on the revision's branch (empty for old kinds).
	CommitSHA string `json:"commit_sha,omitempty"`
}

// checkpoint records every manifest file (its operation, markdown, and a
// snapshot of its document, kept through compaction) for the commit sha
// that saved them. Rooms are flushed already.
func (h *Hub) checkpoint(ctx context.Context, rev revisions.Revision, files []revisions.File, by, name, sha, hash string) (CheckpointView, error) {
	type snap struct {
		docID string
		state []byte
	}
	snaps := map[string]snap{}
	for _, f := range files {
		if f.Op == revisions.OpDelete {
			continue
		}
		docID, state, err := h.stateOf(ctx, rev.ID, f.Path)
		if err != nil {
			return CheckpointView{}, err
		}
		if state != nil {
			snaps[f.Path] = snap{docID, state}
		}
	}
	now := time.Now()
	cp := CheckpointView{ID: ids.New(ids.Checkpoint), Name: strings.TrimSpace(name), Kind: CheckpointSave, CreatedBy: by, CreatedAt: now.UTC(), Files: len(files), CommitSHA: sha}
	err := h.DB.InTx(ctx, func(tx *store.Tx) error {
		var creator any
		if by != "" {
			creator = by
		}
		if _, err := store.Exec(ctx, tx, `INSERT INTO revision_checkpoints (id, revision_id, name, kind, created_by, created_at, commit_sha, content_hash) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			cp.ID, rev.ID, cp.Name, CheckpointSave, creator, store.Millis(now), sha, hash); err != nil {
			return err
		}
		for _, f := range files {
			var snapID any
			if s, ok := snaps[f.Path]; ok {
				id := ids.New(ids.YSnapshot)
				// update_seq -1 keeps this snapshot out of room loads (they
				// start from the newest snapshot); it only serves the checkpoint.
				if _, err := store.Exec(ctx, tx, `INSERT INTO ydoc_snapshots (id, ydoc_id, state, update_seq, created_at) VALUES (?, ?, ?, -1, ?)`,
					id, s.docID, s.state, store.Millis(now)); err != nil {
					return err
				}
				snapID = id
			}
			if _, err := store.Exec(ctx, tx, `INSERT INTO revision_checkpoint_files (checkpoint_id, path, op, from_path, ydoc_snapshot_id, content_md) VALUES (?, ?, ?, ?, ?, ?)`,
				cp.ID, f.Path, f.Op, f.FromPath, snapID, f.ContentMD); err != nil {
				return err
			}
		}
		return revisions.Record(ctx, tx, rev.ID, revisions.ActorUser, by, "saved", map[string]any{"checkpoint": cp.ID, "sha": sha, "message": cp.Name})
	})
	return cp, err
}

// CheckpointFile is one file as it was at a checkpoint.
type CheckpointFile struct {
	Path     string `json:"path"`
	Op       string `json:"op"`
	FromPath string `json:"from_path,omitempty"`
	Content  string `json:"content,omitempty"`
}

func checkpointFiles(ctx context.Context, q store.Querier, cpID string) ([]CheckpointFile, error) {
	rows, err := store.Query(ctx, q, `SELECT path, op, from_path, content_md FROM revision_checkpoint_files WHERE checkpoint_id = ? ORDER BY path`, cpID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CheckpointFile{}
	for rows.Next() {
		var f CheckpointFile
		if err := rows.Scan(&f.Path, &f.Op, &f.FromPath, &f.Content); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Restore brings the revision back to a checkpoint as a new change (for the
// next Save all to commit): unsaved work is saved first, then the manifest is reconciled and every page's
// content applied through its room, so open editors follow along.
func (h *Hub) Restore(ctx context.Context, repo repos.Repo, rev revisions.Revision, c revisions.Caller, cpID string) error {
	h.init()
	acc, err := h.Revisions.AccessFor(ctx, rev, c)
	if err != nil {
		return err
	}
	if !acc.CanEdit {
		return revisions.ErrForbidden
	}
	var n int
	if err := store.QueryRow(ctx, h.DB, `SELECT COUNT(*) FROM revision_checkpoints WHERE id = ? AND revision_id = ?`, cpID, rev.ID).Scan(&n); err != nil || n == 0 {
		return store.ErrNotFound
	}
	target, err := checkpointFiles(ctx, h.DB, cpID)
	if err != nil {
		return err
	}
	// Unsaved work is committed first, so the restore can be undone.
	if err := h.SaveFirst(ctx, repo, rev, c.User, "Save before restoring a checkpoint"); err != nil {
		return err
	}
	current, err := revisions.Files(ctx, h.DB, rev.ID)
	if err != nil {
		return err
	}
	want := map[string]CheckpointFile{}
	for _, f := range target {
		want[f.Path] = f
	}
	op := func(o revisions.FileOp) error {
		_, err := h.Revisions.ApplyFileOp(ctx, repo, rev, c, o)
		return err
	}
	// Changes made after the checkpoint: undo them.
	for _, f := range current {
		if _, ok := want[f.Path]; ok {
			continue
		}
		switch f.Op {
		case revisions.OpAdd:
			err = op(revisions.FileOp{Op: revisions.OpDelete, Path: f.Path})
		case revisions.OpModify:
			err = h.Apply(ctx, repo, rev, c, f.Path, f.BaseMD, "restore")
		case revisions.OpRename:
			if _, stillWanted := want[f.FromPath]; stillWanted {
				continue // the checkpoint has the original path; handled below
			}
			if err = op(revisions.FileOp{Op: revisions.OpRename, FromPath: f.Path, Path: f.FromPath}); err == nil {
				err = h.Apply(ctx, repo, rev, c, f.FromPath, f.BaseMD, "restore")
			}
		case revisions.OpDelete:
			err = op(revisions.FileOp{Op: revisions.OpAdd, Path: f.Path, Content: f.BaseMD})
		}
		if err != nil {
			return err
		}
	}
	// The checkpoint's files, as they were.
	cur := map[string]revisions.File{}
	if current, err = revisions.Files(ctx, h.DB, rev.ID); err != nil {
		return err
	}
	for _, f := range current {
		cur[f.Path] = f
	}
	for _, f := range target {
		have, ok := cur[f.Path]
		switch {
		case f.Op == revisions.OpDelete:
			if !ok || have.Op != revisions.OpDelete {
				err = op(revisions.FileOp{Op: revisions.OpDelete, Path: f.Path})
			}
		case !ok && f.Op == revisions.OpAdd:
			err = op(revisions.FileOp{Op: revisions.OpAdd, Path: f.Path, Content: f.Content})
		case !ok && f.Op == revisions.OpRename:
			if err = op(revisions.FileOp{Op: revisions.OpRename, FromPath: f.FromPath, Path: f.Path}); err == nil {
				err = h.Apply(ctx, repo, rev, c, f.Path, f.Content, "restore")
			}
		case ok && have.Op == revisions.OpDelete:
			err = op(revisions.FileOp{Op: revisions.OpAdd, Path: f.Path, Content: f.Content})
		default:
			if !ok {
				if _, err = h.Revisions.EnsureFile(ctx, repo, rev, c, f.Path); err != nil {
					return err
				}
			}
			if !ok || have.ContentMD != f.Content {
				err = h.Apply(ctx, repo, rev, c, f.Path, f.Content, "restore")
			}
		}
		if err != nil {
			return err
		}
	}
	for _, r := range h.liveRooms(rev.ID) {
		r.flush(ctx)
		r.materialize(ctx)
	}
	return revisions.Record(ctx, h.DB, rev.ID, revisions.ActorUser, c.User.ID, "restored", map[string]any{"checkpoint": cpID})
}

// Routes registers checkpoint endpoints.
func (h *Hub) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.Require)
		r.Get("/revisions/{revision}/checkpoints", h.listCheckpoints)
		r.Post("/revisions/{revision}/save", h.save)
		r.Get("/revisions/{revision}/checkpoints/{checkpoint}/files", h.checkpointFiles)
		r.Get("/revisions/{revision}/checkpoints/{checkpoint}/files/*", h.checkpointFile)
		r.Post("/revisions/{revision}/checkpoints/{checkpoint}/restore", h.restore)
		r.Get("/revisions/{revision}/presence", h.presence)
		r.Get("/revisions/{revision}/sourcemap/*", h.sourceMap)
		r.Get("/revisions/{revision}/suggestions", h.listSuggestions)
		r.Post("/revisions/{revision}/suggestions/resolve", h.resolveSuggestions)
	})
}

func (h *Hub) load(w http.ResponseWriter, r *http.Request) (revisions.Revision, repos.Repo, revisions.Caller, bool) {
	p, _ := auth.FromContext(r.Context())
	rev, err := revisions.Get(r.Context(), h.DB, chi.URLParam(r, "revision"))
	if err != nil {
		api.Error(w, r, api.ErrNotFound)
		return rev, repos.Repo{}, revisions.Caller{}, false
	}
	repo, err := repos.Get(r.Context(), h.DB, rev.RepoID)
	if err != nil {
		api.Error(w, r, err)
		return rev, repo, revisions.Caller{}, false
	}
	role, err := access.Effective(r.Context(), h.DB, p.User, repo.ID)
	if err != nil {
		api.Error(w, r, err)
		return rev, repo, revisions.Caller{}, false
	}
	if role == access.None {
		api.Error(w, r, api.ErrNotFound)
		return rev, repo, revisions.Caller{}, false
	}
	return rev, repo, revisions.Caller{User: p.User, Role: role}, true
}

func (h *Hub) listCheckpoints(w http.ResponseWriter, r *http.Request) {
	rev, _, _, ok := h.load(w, r)
	if !ok {
		return
	}
	rows, err := store.Query(r.Context(), h.DB, `SELECT c.id, c.name, c.kind, COALESCE(c.created_by, ''), COALESCE(u.name, ''), c.created_at,
		(SELECT COUNT(*) FROM revision_checkpoint_files f WHERE f.checkpoint_id = c.id), c.commit_sha
		FROM revision_checkpoints c LEFT JOIN users u ON u.id = c.created_by WHERE c.revision_id = ? ORDER BY c.created_at DESC, c.id DESC`, rev.ID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	defer rows.Close()
	out := []CheckpointView{}
	for rows.Next() {
		var v CheckpointView
		var at int64
		if err := rows.Scan(&v.ID, &v.Name, &v.Kind, &v.CreatedBy, &v.CreatedByName, &at, &v.Files, &v.CommitSHA); err != nil {
			api.Error(w, r, err)
			return
		}
		v.CreatedAt = store.FromMillis(at)
		out = append(out, v)
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Hub) ownCheckpoint(w http.ResponseWriter, r *http.Request, rev revisions.Revision) (string, bool) {
	id := chi.URLParam(r, "checkpoint")
	var n int
	if err := store.QueryRow(r.Context(), h.DB, `SELECT COUNT(*) FROM revision_checkpoints WHERE id = ? AND revision_id = ?`, id, rev.ID).Scan(&n); err != nil || n == 0 {
		api.Error(w, r, api.ErrNotFound)
		return "", false
	}
	return id, true
}

func (h *Hub) checkpointFiles(w http.ResponseWriter, r *http.Request) {
	rev, _, _, ok := h.load(w, r)
	if !ok {
		return
	}
	id, ok := h.ownCheckpoint(w, r, rev)
	if !ok {
		return
	}
	files, err := checkpointFiles(r.Context(), h.DB, id)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	for i := range files {
		files[i].Content = "" // listing only
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": files})
}

func (h *Hub) checkpointFile(w http.ResponseWriter, r *http.Request) {
	rev, _, _, ok := h.load(w, r)
	if !ok {
		return
	}
	id, ok := h.ownCheckpoint(w, r, rev)
	if !ok {
		return
	}
	p := chi.URLParam(r, "*")
	if u, err := url.PathUnescape(p); err == nil {
		p = u
	}
	p = strings.TrimPrefix(path.Clean("/"+p), "/")
	var f CheckpointFile
	err := store.QueryRow(r.Context(), h.DB, `SELECT path, op, from_path, content_md FROM revision_checkpoint_files WHERE checkpoint_id = ? AND path = ?`, id, p).Scan(&f.Path, &f.Op, &f.FromPath, &f.Content)
	if err != nil {
		api.Error(w, r, api.ErrNotFound)
		return
	}
	api.JSON(w, http.StatusOK, f)
}

func (h *Hub) restore(w http.ResponseWriter, r *http.Request) {
	rev, repo, c, ok := h.load(w, r)
	if !ok {
		return
	}
	id, ok := h.ownCheckpoint(w, r, rev)
	if !ok {
		return
	}
	if err := h.Restore(r.Context(), repo, rev, c, id); err != nil {
		switch {
		case errors.Is(err, revisions.ErrForbidden):
			api.Error(w, r, api.Err(http.StatusForbidden, "forbidden", "You can't edit this revision right now."))
		default:
			var cf *revisions.ErrConflict
			if errors.As(err, &cf) {
				api.Error(w, r, api.Err(http.StatusConflict, cf.Code, cf.Msg))
				return
			}
			api.Error(w, r, err)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
