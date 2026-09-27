package revisions

import (
	"context"
	"sync"
	"sync/atomic"
)

type revisionGates struct{ sync.Map }
type gateKey struct{}
type heldGate struct {
	service *Service
	id      string
	active  atomic.Bool
}

// Gate serializes a revision's mutations and publish claim. Lock order is
// revision gate, branch lock, room work, then short room/DB locks. Pass its
// context through synchronous nested calls; never pass it to a goroutine.
func (s *Service) Gate(ctx context.Context, id string) (context.Context, func(), error) {
	if held, ok := ctx.Value(gateKey{}).(*heldGate); ok && held.service == s && held.id == id && held.active.Load() {
		return ctx, func() {}, nil
	}
	candidate := make(chan struct{}, 1)
	value, _ := s.gates.LoadOrStore(id, candidate)
	gate := value.(chan struct{})
	select {
	case gate <- struct{}{}:
	case <-ctx.Done():
		return ctx, func() {}, ctx.Err()
	}
	held := &heldGate{service: s, id: id}
	held.active.Store(true)
	return context.WithValue(ctx, gateKey{}, held), func() {
		if held.active.Swap(false) {
			<-gate
		}
	}, nil
}

// Mutate reloads state under Gate. A caller's earlier permission check must
// never let accepted edits cross a publish claim.
func (s *Service) Mutate(ctx context.Context, id string) (context.Context, Revision, func(), error) {
	ctx, unlock, err := s.Gate(ctx, id)
	if err != nil {
		return ctx, Revision{}, unlock, err
	}
	rev, err := Get(ctx, s.DB, id)
	if err == nil {
		switch rev.State {
		case Editing, InReview, Approved:
		case Publishing:
			err = conflict("readonly", "This revision is read-only. Your change was not saved.")
		default:
			err = ErrForbidden
		}
	}
	if err != nil {
		unlock()
		return ctx, rev, func() {}, err
	}
	return ctx, rev, unlock, nil
}
