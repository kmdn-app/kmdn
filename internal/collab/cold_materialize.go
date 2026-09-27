package collab

import (
	"context"
	"fmt"

	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
)

// materializePersistedRevision repairs the crash window between durable Yjs
// updates and their markdown/approval state. Callers hold the revision gate.
func (h *Hub) materializePersistedRevision(ctx context.Context, revID string) error {
	rev, err := revisions.Get(ctx, h.DB, revID)
	if err != nil {
		return err
	}
	editable := rev.State == revisions.Editing || rev.State == revisions.InReview || rev.State == revisions.Approved
	files, err := revisions.Files(ctx, h.DB, revID)
	if err != nil {
		return err
	}
	for _, f := range files {
		if f.Op == revisions.OpDelete {
			continue
		}
		docID, state, err := h.stateOf(ctx, revID, f.Path)
		if err != nil {
			return err
		}
		if state == nil {
			continue
		}
		var sourceMap string
		if err := store.QueryRow(ctx, h.DB, `SELECT source_map FROM ydocs WHERE id = ?`, docID).Scan(&sourceMap); err != nil {
			return err
		}
		md, err := h.Engine.YMaterialize(ctx, state, sourceMap)
		if err != nil {
			return err
		}
		conflicts, err := h.Engine.YConflicts(ctx, state)
		if err != nil {
			return err
		}
		if !editable {
			if md != f.ContentMD || (conflicts > 0 && !f.HasConflicts) {
				return fmt.Errorf("saved document %s differs from the frozen revision", f.Path)
			}
			continue
		}
		// A cold document may have been compacted since the last materialized
		// edit. Without reliable attribution, reset every stale approval.
		if err := h.Revisions.SetContent(ctx, revID, f.Path, md, nil); err != nil {
			return err
		}
		if err := h.Revisions.SetConflicts(ctx, revID, f.Path, conflicts > 0); err != nil {
			return err
		}
	}
	if editable {
		// SetContent can be a no-op if the crash followed its markdown write
		// but preceded ContentChanged. Matching approval hashes remain valid.
		return h.Revisions.ContentChanged(ctx, revID, nil)
	}
	return nil
}
