package collab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/branches"
	"github.com/kmdn-app/kmdn/internal/gitmirror"
	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/users"
)

// ErrNothingToSave: the content is what the last Save all committed.
var ErrNothingToSave = &revisions.ErrConflict{Code: "nothing_to_save", Msg: "All changes are saved."}

// Save is Save all (docs/specs/05-collaboration.md#saving-and-checkpoints):
// it commits the revision's current content on its branch, authored by u,
// pushes it to the pull request, and records the commit as a checkpoint.
// message is the commit's title and body; empty names the changed pages.
func (h *Hub) Save(ctx context.Context, repo repos.Repo, rev revisions.Revision, u users.User, message string) (CheckpointView, error) {
	h.init()
	ctx, current, release, err := h.Revisions.Mutate(ctx, rev.ID)
	if err != nil {
		return CheckpointView{}, err
	}
	defer release()
	unlock := h.Branches.Lock(rev.ID)
	defer unlock()
	return h.SaveLocked(ctx, repo, current, u, message)
}

// SaveLocked is Save for callers that hold the branch lock (publish).
func (h *Hub) SaveLocked(ctx context.Context, repo repos.Repo, rev revisions.Revision, u users.User, message string) (CheckpointView, error) {
	h.init()
	if cp, recovered, err := h.RecoverSaveLocked(ctx, repo, rev); err != nil || recovered {
		return cp, err
	}
	var err error
	rev, err = revisions.Get(ctx, h.DB, rev.ID)
	if err != nil {
		return CheckpointView{}, err
	}
	if err := h.FlushRevisionChecked(ctx, rev.ID); err != nil {
		return CheckpointView{}, err
	}
	files, payload, err := h.captureSave(ctx, rev.ID)
	if err != nil {
		return CheckpointView{}, err
	}
	unsaved, err := revisions.Unsaved(ctx, h.DB, rev.ID)
	if err != nil {
		return CheckpointView{}, err
	}
	if !unsaved {
		return CheckpointView{}, ErrNothingToSave
	}
	hash := revisions.SnapshotHash(files, payload.Assets)
	saved, err := revisions.SavedHash(ctx, h.DB, rev.ID)
	if err != nil {
		return CheckpointView{}, err
	}
	name := strings.TrimSpace(message)
	title := name
	switch {
	case title != "":
	case hash == saved:
		title = "Merge updates from Published"
	default:
		title = saveTitle(files)
	}
	rev, err = h.Branches.Ensure(ctx, repo, rev)
	if err != nil {
		return CheckpointView{}, err
	}
	changes, _, err := h.Branches.SnapshotChanges(files, payload.Assets)
	if err != nil {
		return CheckpointView{}, err
	}
	msg := title + "\n\nKmdn-Revision: " + h.Branches.RevisionURL(repo, rev) + "\n"
	payload.Author = gitmirror.Identity{Name: u.Name, Email: branches.CommitEmail(ctx, h.DB, u, repo.ForgeHostID)}
	sha, objects, err := h.Branches.PrepareSave(ctx, repo, rev, changes, msg, payload.Author)
	if err != nil {
		return CheckpointView{}, err
	}
	cp := CheckpointView{ID: ids.New(ids.Checkpoint), Name: name, Kind: CheckpointSave, CreatedBy: u.ID, CreatedByName: u.Name, CreatedAt: time.Now().UTC(), Files: len(files), CommitSHA: sha}
	intent := saveIntent{Checkpoint: cp, Branch: rev.Branch, PriorSHA: rev.BranchSHA, BaseSHA: rev.BaseSHA, Hash: hash, Objects: objects, Payload: payload}
	if err := h.putSaveIntent(ctx, rev.ID, intent); err != nil {
		return CheckpointView{}, err
	}
	cp, _, err = h.RecoverSaveLocked(ctx, repo, rev)
	return cp, err
}

// SaveFirst saves unsaved work before an action that replaces content
// (submit, applying updates, restoring). Nothing to save is fine.
func (h *Hub) SaveFirst(ctx context.Context, repo repos.Repo, rev revisions.Revision, u users.User, message string) error {
	ctx, current, release, err := h.Revisions.Mutate(ctx, rev.ID)
	if err != nil {
		return err
	}
	defer release()
	unlock := h.Branches.Lock(rev.ID)
	defer unlock()
	return h.SaveCurrentLocked(ctx, repo, current, u, message)
}

// saveTitle names what changed: "Update onboarding.md", "Add faq.md and
// update index.md", "Update 5 pages".
func saveTitle(files []revisions.File) string {
	if len(files) == 0 {
		return "Update images"
	}
	if len(files) > 3 {
		return fmt.Sprintf("Update %d pages", len(files))
	}
	var parts []string
	for i, f := range files {
		verb := map[string]string{revisions.OpAdd: "add", revisions.OpDelete: "delete", revisions.OpRename: "move"}[f.Op]
		if verb == "" {
			verb = "update"
		}
		if i == 0 {
			verb = strings.ToUpper(verb[:1]) + verb[1:]
		}
		part := verb + " " + path.Base(f.Path)
		if f.Op == revisions.OpRename {
			part = verb + " " + path.Base(f.FromPath) + " to " + f.Path
		}
		parts = append(parts, part)
	}
	switch len(parts) {
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	}
	return parts[0] + ", " + parts[1] + " and " + parts[2]
}

func (h *Hub) save(w http.ResponseWriter, r *http.Request) {
	rev, repo, c, ok := h.load(w, r)
	if !ok {
		return
	}
	var in struct {
		Message string `json:"message"`
	}
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	if len(in.Message) > 2000 {
		api.Error(w, r, api.Invalid("message", "Keep the message under 2,000 characters."))
		return
	}
	acc, err := h.Revisions.AccessFor(r.Context(), rev, c)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	if !acc.CanEdit {
		api.Error(w, r, api.Err(http.StatusForbidden, "forbidden", "Only people who can edit the revision can save it."))
		return
	}
	cp, err := h.Save(r.Context(), repo, rev, c.User, in.Message)
	var cf *revisions.ErrConflict
	switch {
	case errors.As(err, &cf):
		api.Error(w, r, api.Err(http.StatusConflict, cf.Code, cf.Msg))
	case errors.Is(err, branches.ErrBranchMoved):
		api.Error(w, r, api.Err(http.StatusConflict, "branch_moved", "The revision's branch changed outside kmdn, so kmdn won't overwrite it."))
	case err != nil:
		h.Log.Error("save", "err", err, "revision", rev.ID)
		api.Error(w, r, api.Err(http.StatusBadGateway, "forge_unavailable", "Couldn't save to the forge. Your changes are kept; try again."))
	default:
		api.JSON(w, http.StatusCreated, cp)
	}
}
