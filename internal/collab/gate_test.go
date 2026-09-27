package collab

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/storetest"
)

func gatedRoom(t *testing.T) (*Hub, *Room) {
	t.Helper()
	db := storetest.Open(t)
	for _, query := range []string{
		`INSERT INTO forge_hosts (id, kind, display_name, created_at) VALUES ('host_gate', 'git', 'Git', 1)`,
		`INSERT INTO repos (org_id, id, forge_host_id, owner, name, display_name, target_branch, created_at) VALUES ('org_default', 'repo_gate', 'host_gate', 'owner', 'repo', 'Repo', 'main', 1)`,
		`INSERT INTO revisions (id, repo_id, number, title, state, base_sha, created_at, updated_at) VALUES ('rev_gate', 'repo_gate', 1, 'Gate', 'approved', 'base', 1, 1)`,
	} {
		if _, err := store.Exec(context.Background(), db, query); err != nil {
			t.Fatal(err)
		}
	}
	h := &Hub{DB: db, Revisions: &revisions.Service{DB: db}, Options: Options{EvictAfter: time.Hour}}
	h.init()
	r := &Room{hub: h, revID: "rev_gate", path: "docs/index.md", ready: make(chan struct{}), peers: map[*Peer]struct{}{}, clients: map[uint64]string{}}
	close(r.ready)
	h.rooms[key(r.revID, r.path)] = r
	return h, r
}

func TestIngestRechecksStateAfterWaitingForPublishGate(t *testing.T) {
	h, r := gatedRoom(t)
	ctx, unlock, err := h.Revisions.Gate(context.Background(), r.revID)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- r.ingest(context.Background(), []byte{1, 2, 3}, nil, revisions.Caller{}, nil, "human")
	}()
	// The update carries earlier write permission but cannot enter while the
	// publisher owns the gate. Commit the claim before allowing it through.
	if _, err := store.Exec(ctx, h.DB, `UPDATE revisions SET state = 'publishing' WHERE id = ?`, r.revID); err != nil {
		t.Fatal(err)
	}
	unlock()
	var conflict *revisions.ErrConflict
	if err := <-result; !errors.As(err, &conflict) || conflict.Code != "readonly" {
		t.Fatalf("late update: %v", err)
	}
	if r.seq != 0 || len(r.tail) != 0 || len(r.pending) != 0 || r.dirty {
		t.Fatal("rejected update changed shared document state")
	}
}

func TestEvictionKeepsRoomVisibleUntilGatedFlushCompletes(t *testing.T) {
	h, r := gatedRoom(t)
	r.work.Lock()
	done := make(chan struct{})
	go func() { r.evict(); close(done) }()
	// Wait until eviction owns the gate and is blocked at the work lock.
	// Successful probes release immediately; a timeout proves ownership.
	deadline := time.Now().Add(5 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		_, unlock, err := h.Revisions.Gate(ctx, r.revID)
		cancel()
		if err != nil {
			break
		}
		unlock()
		if time.Now().After(deadline) {
			r.work.Unlock()
			t.Fatal("eviction never entered the gate")
		}
	}
	rooms := h.liveRooms(r.revID)
	if len(rooms) != 1 || rooms[0] != r {
		t.Error("eviction hid the room before its accepted edits were flushed")
	}
	r.mu.Lock()
	r.closed = true // no persistence is needed to release this test's work lock
	r.mu.Unlock()
	r.work.Unlock()
	<-done
}
