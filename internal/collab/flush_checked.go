package collab

import (
	"context"
	"errors"
)

// FlushRevisionChecked refuses a save when an accepted update is still only
// in memory, or its markdown/conflict state could not be materialized.
func (h *Hub) FlushRevisionChecked(ctx context.Context, revID string) error {
	ctx, unlock, err := h.Revisions.Gate(ctx, revID)
	if err != nil {
		return err
	}
	defer unlock()
	h.FlushRevision(ctx, revID)
	for _, room := range h.liveRooms(revID) {
		room.mu.Lock()
		pending := !room.closed && (len(room.pending) != 0 || room.dirty)
		room.mu.Unlock()
		if pending {
			return errors.New("accepted changes could not be saved; retry when the document service is available")
		}
	}
	return h.materializePersistedRevision(ctx, revID)
}
