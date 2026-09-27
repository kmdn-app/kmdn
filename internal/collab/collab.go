// Package collab runs the server side of collaborative editing: one room per
// revision file, holding the Y.Doc as encoded updates. The CRDT math runs in
// the doc engine (Yjs inside goja) on demand, so rooms keep no JS state.
// See docs/specs/05-collaboration.md#server-side-room.
package collab

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"math/rand/v2"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/branches"
	"github.com/kmdn-app/kmdn/internal/docengine"
	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/realtime"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/telemetry"
)

// Options tunes rooms; zero values use the spec's defaults.
type Options struct {
	FlushDelay   time.Duration // batch update inserts (50 ms)
	QuietPeriod  time.Duration // materialize after this long without updates (2 s)
	EvictAfter   time.Duration // drop an empty room from memory (5 min)
	CompactEvery int           // snapshot after this many updates (500)
	MaxUpdate    int           // bytes per update (1 MiB)
	MaxPeers     int           // concurrent editors per file (50)
}

func (o *Options) defaults() {
	if o.FlushDelay <= 0 {
		o.FlushDelay = 50 * time.Millisecond
	}
	if o.QuietPeriod <= 0 {
		o.QuietPeriod = 2 * time.Second
	}
	if o.EvictAfter <= 0 {
		o.EvictAfter = 5 * time.Minute
	}
	if o.CompactEvery <= 0 {
		o.CompactEvery = 500
	}
	if o.MaxUpdate <= 0 {
		o.MaxUpdate = 1 << 20
	}
	if o.MaxPeers <= 0 {
		o.MaxPeers = 50
	}
}

// Hub owns the rooms.
type Hub struct {
	DB        *store.DB
	Engine    *docengine.Engine
	Revisions *revisions.Service
	// Branches commits Save all to the revision's branch.
	Branches *branches.Service
	Log      *slog.Logger
	// Publish fans out events (realtime.Hub.Publish). Optional.
	Publish func(scope string, event map[string]any)
	Options Options

	once  sync.Once
	mu    sync.Mutex
	rooms map[string]*Room // by revision id + "\x00" + path
}

const maxAwarenessState = 8 << 10

func key(revID, p string) string { return revID + "\x00" + p }

func (h *Hub) init() {
	h.once.Do(func() {
		h.Options.defaults()
		h.rooms = map[string]*Room{}
	})
}

func joinErr(code, msg string) error { return &realtime.JoinError{Code: code, Message: msg} }

// Join implements realtime.Rooms.
func (h *Hub) Join(ctx context.Context, c *realtime.Conn, channel uint32, ref realtime.RoomRef) (realtime.Channel, string, string, error) {
	h.init()
	rev, err := revisions.Get(ctx, h.DB, ref.Revision)
	if errors.Is(err, store.ErrNotFound) {
		return nil, "", "", joinErr("not_found", "This revision doesn't exist.")
	} else if err != nil {
		return nil, "", "", err
	}
	repo, err := repos.Get(ctx, h.DB, rev.RepoID)
	if err != nil {
		return nil, "", "", err
	}
	role, err := access.Effective(ctx, h.DB, c.User, repo.ID)
	if err != nil {
		return nil, "", "", err
	}
	if role == access.None {
		return nil, "", "", joinErr("not_found", "This revision doesn't exist.")
	}
	caller := revisions.Caller{User: c.User, Role: role}
	acc, err := h.Revisions.AccessFor(ctx, rev, caller)
	if err != nil {
		return nil, "", "", err
	}
	p := strings.TrimPrefix(path.Clean("/"+ref.Path), "/")
	if !repos.IsMarkdown(p) || !repo.Scope().Contains(p) {
		return nil, "", "", joinErr("not_found", "This page isn't part of the repository's content.")
	}
	room, err := h.room(ctx, repo, rev, p, acc.CanEdit, c.User.ID)
	if err != nil {
		return nil, "", "", err
	}
	peer, err := room.add(c, channel, caller, acc.CanEdit)
	if err != nil {
		return nil, "", "", err
	}
	mode := "ro"
	if acc.CanEdit {
		mode = "rw"
	}
	return peer, mode, acc.Reason, nil
}

// room returns the loaded room, loading or creating its document. Loads for
// different files run in parallel; joins for the same file wait for one load.
func (h *Hub) room(ctx context.Context, repo repos.Repo, rev revisions.Revision, p string, canCreate bool, userID string) (*Room, error) {
	k := key(rev.ID, p)
	for {
		h.mu.Lock()
		r := h.rooms[k]
		if r == nil {
			r = &Room{hub: h, revID: rev.ID, path: p, ready: make(chan struct{})}
			h.rooms[k] = r
			h.mu.Unlock()
			r.loadErr = r.load(ctx, repo, rev, canCreate, userID)
			close(r.ready)
			if r.loadErr != nil {
				h.mu.Lock()
				if h.rooms[k] == r {
					delete(h.rooms, k)
				}
				h.mu.Unlock()
				return nil, r.loadErr
			}
			return r, nil
		}
		h.mu.Unlock()
		select {
		case <-r.ready:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if r.loadErr != nil {
			if !canCreate {
				return nil, r.loadErr
			}
			continue // the loader couldn't create it; we may
		}
		r.mu.Lock()
		gone := r.evicted
		r.mu.Unlock()
		if !gone {
			return r, nil
		}
	}
}

// RevisionChanged re-evaluates what each peer may do after the revision
// changed state, and picks up renames. Wire it to revisions.Service.Changed.
func (h *Hub) RevisionChanged(ctx context.Context, rev revisions.Revision) {
	h.init()
	paths := map[string]string{} // ydoc id → path
	rows, err := store.Query(ctx, h.DB, `SELECT id, path FROM ydocs WHERE revision_id = ?`, rev.ID)
	if err == nil {
		for rows.Next() {
			var id, p string
			if rows.Scan(&id, &p) == nil {
				paths[id] = p
			}
		}
		_ = rows.Close()
	}
	h.mu.Lock()
	var rooms []*Room
	for k, r := range h.rooms {
		if r.revID != rev.ID {
			continue
		}
		select {
		case <-r.ready:
		default:
			continue
		}
		if np, ok := paths[r.docID]; ok && np != r.path {
			delete(h.rooms, k)
			r.mu.Lock()
			r.path = np
			r.mu.Unlock()
			h.rooms[key(rev.ID, np)] = r
		}
		rooms = append(rooms, r)
	}
	h.mu.Unlock()
	for _, r := range rooms {
		r.refreshModes(ctx, rev)
	}
	if len(rooms) > 0 {
		h.publishPresence(rev.ID)
	}
}

// FileRemoved closes a room whose document was deleted (the page was deleted
// or the added page removed). Wire it to revisions.Service.Removed.
func (h *Hub) FileRemoved(rev revisions.Revision, p string) {
	h.init()
	h.mu.Lock()
	r := h.rooms[key(rev.ID, p)]
	delete(h.rooms, key(rev.ID, p))
	h.mu.Unlock()
	if r != nil {
		r.shutdown("deleted")
	}
}

// Rooms counts the documents open in memory (metrics).
func (h *Hub) Rooms() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.rooms)
}

// Flush persists and materializes every room (shutdown, tests).
func (h *Hub) Flush(ctx context.Context) {
	h.init()
	h.mu.Lock()
	rooms := make([]*Room, 0, len(h.rooms))
	for _, r := range h.rooms {
		rooms = append(rooms, r)
	}
	h.mu.Unlock()
	for _, r := range rooms {
		select {
		case <-r.ready:
			if r.loadErr == nil {
				r.flush(ctx)
				r.materialize(ctx)
			}
		default:
		}
	}
}

// Room is one collaborative document.
type Room struct {
	hub     *Hub
	revID   string
	docID   string
	ready   chan struct{}
	loadErr error

	sourceMap string

	mu         sync.Mutex
	path       string
	peers      map[*Peer]struct{}
	state      []byte   // merged document state
	gen        int      // bumped when state is replaced
	tail       [][]byte // updates since state
	seq        int64    // last assigned update seq
	snapSeq    int64    // seq covered by the latest snapshot
	pending    []pendingUpdate
	clients    map[uint64]string // Yjs client id → user id
	awareness  map[uint64]awarenessEntry
	awOwner    map[uint64]*Peer
	inManifest bool
	dirty      bool            // updates not yet materialized
	editors    map[string]bool // who changed it since the last materialization
	flushT     *time.Timer
	quietT     *time.Timer
	evictT     *time.Timer
	evicted    bool
	closed     bool

	work sync.Mutex // serializes flush/compaction/materialization
}

type pendingUpdate struct {
	seq    int64
	data   []byte
	userID string
	at     int64
}

func (r *Room) load(ctx context.Context, repo repos.Repo, rev revisions.Revision, canCreate bool, userID string) error {
	h := r.hub
	r.peers, r.clients, r.awareness, r.awOwner = map[*Peer]struct{}{}, map[uint64]string{}, map[uint64]awarenessEntry{}, map[uint64]*Peer{}
	var sm string
	err := store.QueryRow(ctx, h.DB, `SELECT id, source_map FROM ydocs WHERE revision_id = ? AND path = ?`, rev.ID, r.path).Scan(&r.docID, &sm)
	if errors.Is(err, sql.ErrNoRows) {
		if !canCreate {
			return joinErr("no_document", "Nobody has edited this page in the revision yet.")
		}
		return r.create(ctx, repo, rev, userID)
	}
	if err != nil {
		return err
	}
	r.sourceMap = sm
	var snap []byte
	err = store.QueryRow(ctx, h.DB, `SELECT state, update_seq FROM ydoc_snapshots WHERE ydoc_id = ? ORDER BY update_seq DESC LIMIT 1`, r.docID).Scan(&snap, &r.snapSeq)
	if err != nil {
		return err
	}
	r.state, r.seq = snap, r.snapSeq
	rows, err := store.Query(ctx, h.DB, `SELECT seq, data FROM ydoc_updates WHERE ydoc_id = ? AND seq > ? ORDER BY seq`, r.docID, r.snapSeq)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var u []byte
		if err := rows.Scan(&r.seq, &u); err != nil {
			return err
		}
		r.tail = append(r.tail, u)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	crows, err := store.Query(ctx, h.DB, `SELECT client_id, user_id FROM ydoc_clients WHERE ydoc_id = ?`, r.docID)
	if err != nil {
		return err
	}
	defer crows.Close()
	for crows.Next() {
		var id int64
		var u string
		if err := crows.Scan(&id, &u); err != nil {
			return err
		}
		r.clients[uint64(id)] = u
	}
	if _, err := revisions.FileAt(ctx, h.DB, rev.ID, r.path); err == nil {
		r.inManifest = true
	}
	return crows.Err()
}

func (r *Room) create(ctx context.Context, repo repos.Repo, rev revisions.Revision, userID string) error {
	h := r.hub
	c, err := h.Revisions.Read(ctx, repo, rev, r.path)
	if err != nil {
		var cf *revisions.ErrConflict
		if errors.As(err, &cf) {
			return joinErr(cf.Code, cf.Msg)
		}
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, repos.ErrOutOfScope) || strings.Contains(err.Error(), "not found") {
			return joinErr("not_found", "This page doesn't exist in the revision.")
		}
		return err
	}
	serverClient := uint64(rand.Uint32())
	update, sm, err := h.Engine.YFromMarkdown(ctx, c.Content, uint32(serverClient))
	if err != nil {
		if errors.Is(err, docengine.ErrTooLarge) {
			return joinErr("too_large", "This page is too large for the visual editor.")
		}
		return err
	}
	r.docID, r.sourceMap, r.state = ids.New(ids.YDoc), sm, update
	now := store.Millis(time.Now())
	err = h.DB.InTx(ctx, func(tx *store.Tx) error {
		if _, err := store.Exec(ctx, tx, `INSERT INTO ydocs (id, revision_id, path, engine_version, source_map, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			r.docID, rev.ID, r.path, h.Engine.Version(), sm, now); err != nil {
			return err
		}
		if _, err := store.Exec(ctx, tx, `INSERT INTO ydoc_snapshots (id, ydoc_id, state, update_seq, created_at) VALUES (?, ?, ?, 0, ?)`,
			ids.New(ids.YSnapshot), r.docID, update, now); err != nil {
			return err
		}
		_, err := store.Exec(ctx, tx, `INSERT INTO ydoc_clients (ydoc_id, client_id, user_id, kind) VALUES (?, ?, ?, 'sync')`, r.docID, int64(serverClient), userID)
		return err
	})
	if err != nil {
		return err
	}
	r.clients[serverClient] = userID
	r.inManifest = c.InRevision
	return nil
}

// Peer is a connection's subscription to a room.
type Peer struct {
	room    *Room
	conn    *realtime.Conn
	channel uint32
	caller  revisions.Caller

	mu     sync.Mutex
	rw     bool
	warned bool
}

func (r *Room) add(c *realtime.Conn, channel uint32, caller revisions.Caller, rw bool) (*Peer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.evicted {
		return nil, joinErr("unavailable", "This document just closed. Try again.")
	}
	if len(r.peers) >= r.hub.Options.MaxPeers {
		return nil, joinErr("room_full", "Too many people have this page open.")
	}
	p := &Peer{room: r, conn: c, channel: channel, caller: caller, rw: rw}
	r.peers[p] = struct{}{}
	if r.evictT != nil {
		r.evictT.Stop()
		r.evictT = nil
	}
	go r.hub.publishPresence(r.revID)
	return p, nil
}

// Start sends sync step 1 (our state vector) and step 2 is answered when the
// client sends its own step 1. Current presence goes out right away.
func (p *Peer) Start() {
	r := p.room
	ctx := p.conn.Context()
	state := r.merged(ctx)
	if state == nil {
		return
	}
	sv, err := r.hub.Engine.YStateVector(ctx, state)
	if err != nil {
		r.hub.Log.Error("state vector", "err", err, "doc", r.docID)
		return
	}
	p.conn.Send(realtime.KindSync, p.channel, syncMsg(syncStep1, sv))
	r.mu.Lock()
	entries := make([]awarenessEntry, 0, len(r.awareness))
	for _, e := range r.awareness {
		entries = append(entries, e)
	}
	r.mu.Unlock()
	if len(entries) > 0 {
		p.conn.Send(realtime.KindAwareness, p.channel, encodeAwareness(entries))
	}
}

// merged folds the tail into the state and returns it.
func (r *Room) merged(ctx context.Context) []byte {
	r.mu.Lock()
	state, tail, gen := r.state, r.tail, r.gen
	r.mu.Unlock()
	if len(tail) == 0 {
		return state
	}
	all := append([][]byte{state}, tail...)
	m, err := r.hub.Engine.YMerge(ctx, all)
	if err != nil {
		r.hub.Log.Error("merge updates", "err", err, "doc", r.docID)
		return nil
	}
	r.mu.Lock()
	// Unless another merge won the race, fold in what we merged and keep the
	// updates that arrived meanwhile.
	if r.gen == gen {
		r.state, r.tail = m, append([][]byte(nil), r.tail[len(tail):]...)
		r.gen++
	}
	r.mu.Unlock()
	return m
}

// Receive implements realtime.Channel.
func (p *Peer) Receive(kind byte, payload []byte) {
	switch kind {
	case realtime.KindSync:
		p.receiveSync(payload)
	case realtime.KindAwareness:
		p.receiveAwareness(payload)
	}
}

func (p *Peer) receiveSync(msg []byte) {
	r := p.room
	ctx := p.conn.Context()
	typ, data, err := parseSync(msg)
	if err != nil {
		return
	}
	switch typ {
	case syncStep1:
		state := r.merged(ctx)
		if state == nil {
			return
		}
		diff, err := r.hub.Engine.YDiff(ctx, state, data)
		if err != nil {
			return
		}
		p.conn.Send(realtime.KindSync, p.channel, syncMsg(syncStep2, diff))
	case syncStep2, syncUpdate:
		p.update(ctx, data)
	}
}

func (p *Peer) refuse(code, msg string) {
	p.mu.Lock()
	warned := p.warned
	p.warned = true
	p.mu.Unlock()
	if !warned {
		p.conn.Control(map[string]any{"op": "error", "channel": p.channel, "code": code, "message": msg})
	}
}

func (p *Peer) update(ctx context.Context, data []byte) {
	r := p.room
	p.mu.Lock()
	rw := p.rw
	p.mu.Unlock()
	if !rw {
		p.refuse("read_only", "Your changes weren't saved: this page is read-only for you right now.")
		return
	}
	if len(data) > r.hub.Options.MaxUpdate {
		p.refuse("too_large", "That change is too large.")
		return
	}
	clients, err := r.hub.Engine.YClients(ctx, data)
	if err != nil {
		p.refuse("bad_update", "The server couldn't read a change from your browser. Reload the page.")
		return
	}
	if len(clients) == 0 && len(data) <= 2 {
		return // an empty update (e.g. step 2 from an up-to-date client); deletions have no clients but more bytes
	}
	if err := r.ingest(ctx, data, clients, p.caller, p, "human"); errors.Is(err, errClientConflict) {
		p.refuse("client_conflict", "Your editor's id collided with someone else's. Reload the page.")
	}
}

var errClientConflict = errors.New("collab: client id belongs to someone else")

// ingest appends a validated update to the room: log, broadcast to everyone
// but from, persistence and materialization timers, client attribution, and
// the manifest entry on the first edit. kind is recorded for new client ids
// (human, restore, assistant).
func (r *Room) ingest(ctx context.Context, data []byte, clients []uint64, c revisions.Caller, from *Peer, kind string) error {
	telemetry.YjsUpdates.Inc()
	uid := c.User.ID
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	var newClients []uint64
	for _, id := range clients {
		if owner, ok := r.clients[id]; ok && owner != uid {
			r.mu.Unlock()
			return errClientConflict
		} else if !ok {
			newClients = append(newClients, id)
		}
	}
	for _, id := range newClients {
		r.clients[id] = uid
	}
	r.seq++
	r.tail = append(r.tail, data)
	r.pending = append(r.pending, pendingUpdate{seq: r.seq, data: data, userID: uid, at: store.Millis(time.Now())})
	r.dirty = true
	if r.editors == nil {
		r.editors = map[string]bool{}
	}
	r.editors[uid] = true
	targets := make([]*Peer, 0, len(r.peers))
	for q := range r.peers {
		if q != from {
			targets = append(targets, q)
		}
	}
	if r.flushT == nil {
		r.flushT = time.AfterFunc(r.hub.Options.FlushDelay, func() { r.flush(context.Background()) })
	}
	if r.quietT != nil {
		r.quietT.Stop()
	}
	r.quietT = time.AfterFunc(r.hub.Options.QuietPeriod, func() { r.materialize(context.Background()) })
	// Comment anchors don't change the page: they don't add it to the revision.
	needManifest := !r.inManifest && kind != "comment"
	r.mu.Unlock()

	out := syncMsg(syncUpdate, data)
	for _, q := range targets {
		q.conn.Send(realtime.KindSync, q.channel, out)
	}
	for _, id := range newClients {
		if _, err := store.Exec(ctx, r.hub.DB, `INSERT INTO ydoc_clients (ydoc_id, client_id, user_id, kind) VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING`, r.docID, int64(id), uid, kind); err != nil {
			r.hub.Log.Error("record client", "err", err, "doc", r.docID)
		}
	}
	if needManifest {
		r.ensureManifest(ctx, c)
	}
	return nil
}

// ensureManifest adds the page to the revision on its first edit.
func (r *Room) ensureManifest(ctx context.Context, c revisions.Caller) {
	rev, err := revisions.Get(ctx, r.hub.DB, r.revID)
	if err != nil {
		return
	}
	repo, err := repos.Get(ctx, r.hub.DB, rev.RepoID)
	if err != nil {
		return
	}
	r.mu.Lock()
	p := r.path
	r.mu.Unlock()
	if _, err := r.hub.Revisions.EnsureFile(ctx, repo, rev, c, p); err != nil {
		r.hub.Log.Warn("add page to revision", "err", err, "revision", r.revID, "path", p)
		return
	}
	r.mu.Lock()
	r.inManifest = true
	dirty := r.dirty
	r.mu.Unlock()
	if dirty {
		// The quiet period may have passed while the page was being added.
		go r.materialize(context.Background())
	}
}

func (p *Peer) receiveAwareness(msg []byte) {
	r := p.room
	entries, err := parseAwareness(msg)
	if err != nil {
		return
	}
	accepted := entries[:0]
	r.mu.Lock()
	for _, e := range entries {
		if len(e.State) > maxAwarenessState {
			continue
		}
		if owner, ok := r.awOwner[e.Client]; ok && owner != p {
			continue // someone else's presence
		}
		if cur, ok := r.awareness[e.Client]; ok && cur.Clock > e.Clock {
			continue
		}
		if e.State == "null" {
			delete(r.awareness, e.Client)
			delete(r.awOwner, e.Client)
		} else {
			r.awareness[e.Client] = e
			r.awOwner[e.Client] = p
		}
		accepted = append(accepted, e)
	}
	targets := make([]*Peer, 0, len(r.peers))
	for q := range r.peers {
		if q != p {
			targets = append(targets, q)
		}
	}
	r.mu.Unlock()
	if len(accepted) == 0 {
		return
	}
	out := encodeAwareness(accepted)
	for _, q := range targets {
		q.conn.Send(realtime.KindAwareness, q.channel, out)
	}
}

// Close implements realtime.Channel: the peer left.
func (p *Peer) Close() {
	r := p.room
	r.mu.Lock()
	if _, ok := r.peers[p]; !ok {
		r.mu.Unlock()
		return
	}
	delete(r.peers, p)
	var gone []awarenessEntry
	for id, owner := range r.awOwner {
		if owner == p {
			gone = append(gone, awarenessEntry{Client: id, Clock: r.awareness[id].Clock + 1, State: "null"})
			delete(r.awareness, id)
			delete(r.awOwner, id)
		}
	}
	targets := make([]*Peer, 0, len(r.peers))
	for q := range r.peers {
		targets = append(targets, q)
	}
	empty := len(r.peers) == 0 && !r.closed
	if empty {
		r.evictT = time.AfterFunc(r.hub.Options.EvictAfter, r.evict)
	}
	r.mu.Unlock()
	go r.hub.publishPresence(r.revID)
	if len(gone) > 0 {
		msg := encodeAwareness(gone)
		for _, q := range targets {
			q.conn.Send(realtime.KindAwareness, q.channel, msg)
		}
	}
	if empty {
		// Save now rather than when the quiet timer fires.
		go func() {
			ctx := context.Background()
			r.flush(ctx)
			r.materialize(ctx)
		}()
	}
}

func (r *Room) evict() {
	h := r.hub
	h.mu.Lock()
	r.mu.Lock()
	if len(r.peers) > 0 || r.evicted {
		r.mu.Unlock()
		h.mu.Unlock()
		return
	}
	r.evicted = true
	k := key(r.revID, r.path)
	r.mu.Unlock()
	if h.rooms[k] == r {
		delete(h.rooms, k)
	}
	h.mu.Unlock()
	ctx := context.Background()
	r.flush(ctx)
	r.materialize(ctx)
}

func (r *Room) shutdown(reason string) {
	r.mu.Lock()
	r.closed = true
	peers := make([]*Peer, 0, len(r.peers))
	for p := range r.peers {
		peers = append(peers, p)
	}
	r.peers = map[*Peer]struct{}{}
	r.pending = nil
	for _, t := range []*time.Timer{r.flushT, r.quietT, r.evictT} {
		if t != nil {
			t.Stop()
		}
	}
	r.mu.Unlock()
	for _, p := range peers {
		p.conn.Detach(p.channel, p)
		p.conn.Control(map[string]any{"op": "closed", "channel": p.channel, "reason": reason})
	}
}

// flush writes pending updates, then compacts when the log is long.
func (r *Room) flush(ctx context.Context) {
	r.work.Lock()
	defer r.work.Unlock()
	r.mu.Lock()
	batch := r.pending
	r.pending = nil
	r.flushT = nil
	closed := r.closed
	since := r.seq - r.snapSeq
	r.mu.Unlock()
	if closed {
		return
	}
	if len(batch) > 0 {
		err := r.hub.DB.InTx(ctx, func(tx *store.Tx) error {
			for _, u := range batch {
				if _, err := store.Exec(ctx, tx, `INSERT INTO ydoc_updates (ydoc_id, seq, data, user_id, created_at) VALUES (?, ?, ?, ?, ?)`, r.docID, u.seq, u.data, u.userID, u.at); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			r.hub.Log.Error("persist updates", "err", err, "doc", r.docID, "count", len(batch))
			r.mu.Lock()
			r.pending = append(batch, r.pending...) // retry on the next flush
			r.mu.Unlock()
			return
		}
	}
	if since >= int64(r.hub.Options.CompactEvery) {
		r.compact(ctx)
	}
}

// compact writes a snapshot and drops the updates it covers. Callers hold work.
func (r *Room) compact(ctx context.Context) {
	r.mu.Lock()
	upTo := r.seq - int64(len(r.pending)) // everything persisted so far
	r.mu.Unlock()
	state := r.merged(ctx)
	if state == nil {
		return
	}
	// merged() may include updates newer than upTo that are still pending;
	// they are persisted by the next flush and replayed idempotently on load.
	err := r.hub.DB.InTx(ctx, func(tx *store.Tx) error {
		if _, err := store.Exec(ctx, tx, `INSERT INTO ydoc_snapshots (id, ydoc_id, state, update_seq, created_at) VALUES (?, ?, ?, ?, ?)`,
			ids.New(ids.YSnapshot), r.docID, state, upTo, store.Millis(time.Now())); err != nil {
			return err
		}
		if _, err := store.Exec(ctx, tx, `DELETE FROM ydoc_updates WHERE ydoc_id = ? AND seq <= ?`, r.docID, upTo); err != nil {
			return err
		}
		// Older snapshots are superseded unless a checkpoint points at them.
		_, err := store.Exec(ctx, tx, `DELETE FROM ydoc_snapshots WHERE ydoc_id = ? AND update_seq < ?
			AND id NOT IN (SELECT ydoc_snapshot_id FROM revision_checkpoint_files WHERE ydoc_snapshot_id IS NOT NULL)`, r.docID, upTo)
		return err
	})
	if err != nil {
		r.hub.Log.Error("compact", "err", err, "doc", r.docID)
		return
	}
	r.mu.Lock()
	r.snapSeq = upTo
	r.mu.Unlock()
}

// materialize stores the page's markdown when it changed since last time.
func (r *Room) materialize(ctx context.Context) {
	r.work.Lock()
	defer r.work.Unlock()
	r.mu.Lock()
	dirty, closed, inManifest, p := r.dirty, r.closed, r.inManifest, r.path
	if !dirty || closed || !inManifest {
		// Not yet in the manifest: stay dirty; ensureManifest materializes
		// once the page has been added.
		r.mu.Unlock()
		return
	}
	r.dirty = false
	by := make([]string, 0, len(r.editors))
	for u := range r.editors {
		by = append(by, u)
	}
	r.editors = nil
	r.mu.Unlock()
	state := r.merged(ctx)
	if state == nil {
		r.markDirty()
		return
	}
	md, err := r.hub.Engine.YMaterialize(ctx, state, r.sourceMap)
	if err != nil {
		r.hub.Log.Error("materialize", "err", err, "doc", r.docID)
		r.markDirty()
		return
	}
	if err := r.hub.Revisions.SetContent(ctx, r.revID, p, md, by); err != nil {
		r.hub.Log.Error("store materialized content", "err", err, "doc", r.docID)
		r.markDirty()
		return
	}
	// Resolving a conflict may leave the markdown as it was (the revision's
	// side is what it materializes to): check the blocks themselves.
	if n, err := r.hub.Engine.YConflicts(ctx, state); err == nil {
		if err := r.hub.Revisions.SetConflicts(ctx, r.revID, p, n > 0); err != nil {
			r.hub.Log.Error("record conflicts", "err", err, "doc", r.docID)
		}
	}
	if r.hub.Publish != nil {
		r.hub.Publish("revision:"+r.revID, map[string]any{"type": "file_content", "revision": r.revID, "path": p})
	}
}

func (r *Room) markDirty() {
	r.mu.Lock()
	r.dirty = true
	r.mu.Unlock()
}

func (r *Room) refreshModes(ctx context.Context, rev revisions.Revision) {
	r.mu.Lock()
	peers := make([]*Peer, 0, len(r.peers))
	for p := range r.peers {
		peers = append(peers, p)
	}
	r.mu.Unlock()
	for _, p := range peers {
		acc, err := r.hub.Revisions.AccessFor(ctx, rev, p.caller)
		if err != nil {
			continue
		}
		p.mu.Lock()
		changed := p.rw != acc.CanEdit
		p.rw, p.warned = acc.CanEdit, false
		p.mu.Unlock()
		if changed {
			mode := "ro"
			if acc.CanEdit {
				mode = "rw"
			}
			p.conn.Control(map[string]any{"op": "mode", "channel": p.channel, "mode": mode, "reason": acc.Reason})
		}
	}
}
