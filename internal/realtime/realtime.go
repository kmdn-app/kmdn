// Package realtime is the multiplexed WebSocket: one connection per browser
// tab carrying collaborative-document channels, awareness and event
// subscriptions. See docs/specs/05-collaboration.md#websocket-protocol.
//
// Frames are binary: [u8 kind][u32 channel, big endian][payload].
package realtime

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/users"
)

// Frame kinds.
const (
	KindControl   byte = 0
	KindSync      byte = 1
	KindAwareness byte = 2
	KindEvent     byte = 3
	KindAgent     byte = 4
)

// Protocol limits.
const (
	MaxFrame      = 2 << 20 // updates are capped at 1 MiB; leave room for framing
	IdleTimeout   = 60 * time.Second
	PingInterval  = 20 * time.Second
	sendQueue     = 512
	writeTimeout  = 10 * time.Second
	maxChannels   = 64
	maxScopes     = 64
	headerLen     = 5
	closeSlowPeer = websocket.StatusPolicyViolation
)

// RoomRef names a collaborative document.
type RoomRef struct {
	Revision string `json:"revision"`
	Path     string `json:"path"`
}

// Channel is one subscription on a connection (a peer in a room).
type Channel interface {
	// Start sends the first frames (sync step 1, current awareness) once the
	// client has been told the channel is subscribed.
	Start()
	// Receive handles a sync or awareness frame from the client.
	Receive(kind byte, payload []byte)
	// Close leaves the room (unsubscribe or disconnect).
	Close()
}

// Rooms opens collaborative-document channels (implemented by collab.Hub).
type Rooms interface {
	Join(ctx context.Context, c *Conn, channel uint32, ref RoomRef) (ch Channel, mode, reason string, err error)
}

// JoinError is a refusal the client can act on.
type JoinError struct{ Code, Message string }

func (e *JoinError) Error() string { return e.Message }

// Hub accepts connections and fans out events.
type Hub struct {
	Rooms Rooms
	// Authorize decides whether a user may receive events for a scope
	// ("repo:<id>", "revision:<id>"; "user" is always allowed).
	Authorize func(ctx context.Context, u users.User, scope string) bool
	// Origin is the only allowed Origin (scheme://host[:port]).
	Origin string
	Log    *slog.Logger

	mu     sync.Mutex
	conns  map[*Conn]struct{}
	scopes map[string]map[*Conn]struct{}
}

// Conn is one WebSocket connection.
type Conn struct {
	ID   string
	User users.User

	hub    *Hub
	ws     *websocket.Conn
	out    chan []byte
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	channels map[uint32]Channel
	scopes   map[string]bool
	closed   bool
}

func (h *Hub) originOK(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return false
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	want, err := url.Parse(h.Origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Scheme, want.Scheme) && strings.EqualFold(u.Host, want.Host)
}

// ServeHTTP upgrades an authenticated request.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.FromContext(r.Context())
	if !ok {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	if !h.originOK(r) {
		http.Error(w, "bad origin", http.StatusForbidden)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true}) // origin checked above
	if err != nil {
		return
	}
	ws.SetReadLimit(MaxFrame)
	// Outlive the HTTP request context (it ends when the handler returns).
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	c := &Conn{ID: ids.New("wsc"), User: p.User, hub: h, ws: ws, out: make(chan []byte, sendQueue), ctx: ctx, cancel: cancel,
		channels: map[uint32]Channel{}, scopes: map[string]bool{}}
	h.mu.Lock()
	if h.conns == nil {
		h.conns, h.scopes = map[*Conn]struct{}{}, map[string]map[*Conn]struct{}{}
	}
	h.conns[c] = struct{}{}
	h.mu.Unlock()
	go c.writeLoop()
	c.readLoop()
	c.shutdown(websocket.StatusNormalClosure, "")
}

func (c *Conn) readLoop() {
	for {
		rctx, cancel := context.WithTimeout(c.ctx, IdleTimeout)
		typ, data, err := c.ws.Read(rctx)
		cancel()
		if err != nil {
			return
		}
		if typ != websocket.MessageBinary || len(data) < headerLen {
			continue
		}
		kind, ch := data[0], binary.BigEndian.Uint32(data[1:headerLen])
		payload := data[headerLen:]
		switch kind {
		case KindControl:
			c.control(payload)
		case KindSync, KindAwareness:
			c.mu.Lock()
			channel := c.channels[ch]
			c.mu.Unlock()
			if channel != nil {
				channel.Receive(kind, payload)
			}
		}
	}
}

func (c *Conn) writeLoop() {
	ping := time.NewTicker(PingInterval)
	defer ping.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case msg := <-c.out:
			wctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
			err := c.ws.Write(wctx, websocket.MessageBinary, msg)
			cancel()
			if err != nil {
				c.shutdown(websocket.StatusGoingAway, "")
				return
			}
		case <-ping.C:
			pctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
			err := c.ws.Ping(pctx)
			cancel()
			if err != nil {
				c.shutdown(websocket.StatusGoingAway, "")
				return
			}
		}
	}
}

func (c *Conn) shutdown(code websocket.StatusCode, reason string) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	chans := c.channels
	c.channels = map[uint32]Channel{}
	scopes := c.scopes
	c.mu.Unlock()
	for _, ch := range chans {
		ch.Close()
	}
	h := c.hub
	h.mu.Lock()
	delete(h.conns, c)
	for s := range scopes {
		if set := h.scopes[s]; set != nil {
			delete(set, c)
			if len(set) == 0 {
				delete(h.scopes, s)
			}
		}
	}
	h.mu.Unlock()
	_ = c.ws.Close(code, reason)
	c.cancel()
}

// Send queues a frame. A peer that can't keep up is disconnected rather than
// buffering without bound; its client reconnects and resyncs.
func (c *Conn) Send(kind byte, channel uint32, payload []byte) {
	msg := make([]byte, headerLen+len(payload))
	msg[0] = kind
	binary.BigEndian.PutUint32(msg[1:headerLen], channel)
	copy(msg[headerLen:], payload)
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return
	}
	select {
	case c.out <- msg:
	default:
		go c.shutdown(closeSlowPeer, "too slow")
	}
}

// Control sends a JSON control message.
func (c *Conn) Control(v any) {
	b, err := json.Marshal(v)
	if err == nil {
		c.Send(KindControl, 0, b)
	}
}

// Context is cancelled when the connection closes.
func (c *Conn) Context() context.Context { return c.ctx }

type controlMsg struct {
	Op      string   `json:"op"`
	Channel uint32   `json:"channel"`
	Room    *RoomRef `json:"room"`
	Scope   string   `json:"scope"`
}

func (c *Conn) control(b []byte) {
	var m controlMsg
	if err := json.Unmarshal(b, &m); err != nil {
		c.Control(map[string]any{"op": "error", "code": "bad_request", "message": "Malformed control message."})
		return
	}
	switch m.Op {
	case "ping":
		c.Control(map[string]any{"op": "pong"})
	case "subscribe":
		c.subscribe(m)
	case "unsubscribe":
		c.mu.Lock()
		ch := c.channels[m.Channel]
		delete(c.channels, m.Channel)
		c.mu.Unlock()
		if ch != nil {
			ch.Close()
		}
	case "subscribe-events":
		c.subscribeEvents(m.Scope)
	case "unsubscribe-events":
		h := c.hub
		h.mu.Lock()
		if set := h.scopes[m.Scope]; set != nil {
			delete(set, c)
		}
		h.mu.Unlock()
		c.mu.Lock()
		delete(c.scopes, m.Scope)
		c.mu.Unlock()
	default:
		c.Control(map[string]any{"op": "error", "code": "unknown_op", "message": "Unknown op " + m.Op})
	}
}

func (c *Conn) subscribe(m controlMsg) {
	fail := func(code, msg string) {
		c.Control(map[string]any{"op": "error", "channel": m.Channel, "code": code, "message": msg})
	}
	if m.Channel == 0 || m.Room == nil || c.hub.Rooms == nil {
		fail("bad_request", "subscribe needs a channel and a room.")
		return
	}
	c.mu.Lock()
	_, taken := c.channels[m.Channel]
	n := len(c.channels)
	c.mu.Unlock()
	if taken {
		fail("channel_in_use", "That channel is already subscribed.")
		return
	}
	if n >= maxChannels {
		fail("too_many_channels", "Too many open documents on this connection.")
		return
	}
	ch, mode, reason, err := c.hub.Rooms.Join(c.ctx, c, m.Channel, *m.Room)
	if err != nil {
		var je *JoinError
		if errors.As(err, &je) {
			fail(je.Code, je.Message)
		} else {
			c.hub.Log.Error("join room", "err", err, "revision", m.Room.Revision, "path", m.Room.Path)
			fail("internal", "Couldn't open the document.")
		}
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		ch.Close()
		return
	}
	c.channels[m.Channel] = ch
	c.mu.Unlock()
	msg := map[string]any{"op": "subscribed", "channel": m.Channel, "mode": mode}
	if reason != "" {
		msg["reason"] = reason
	}
	c.Control(msg)
	ch.Start()
}

// Detach drops a channel without calling Close (the room ended it).
func (c *Conn) Detach(channel uint32, ch Channel) {
	c.mu.Lock()
	if c.channels[channel] == ch {
		delete(c.channels, channel)
	}
	c.mu.Unlock()
}

func (c *Conn) subscribeEvents(scope string) {
	ok := scope == "user" || (c.hub.Authorize != nil && c.hub.Authorize(c.ctx, c.User, scope))
	c.mu.Lock()
	if ok && len(c.scopes) >= maxScopes {
		ok = false
	}
	c.mu.Unlock()
	if !ok {
		c.Control(map[string]any{"op": "error", "scope": scope, "code": "forbidden", "message": "You can't follow that."})
		return
	}
	key := scope
	if scope == "user" {
		key = "user:" + c.User.ID
	}
	h := c.hub
	h.mu.Lock()
	if h.scopes[key] == nil {
		h.scopes[key] = map[*Conn]struct{}{}
	}
	h.scopes[key][c] = struct{}{}
	h.mu.Unlock()
	c.mu.Lock()
	c.scopes[key] = true
	c.mu.Unlock()
	c.Control(map[string]any{"op": "events-subscribed", "scope": scope})
}

// Publish sends an event to every connection following scope. Events are
// JSON objects; the scope is added as "scope".
func (h *Hub) Publish(scope string, event map[string]any) {
	event["scope"] = scope
	b, err := json.Marshal(event)
	if err != nil {
		return
	}
	h.mu.Lock()
	targets := make([]*Conn, 0, len(h.scopes[scope]))
	for c := range h.scopes[scope] {
		targets = append(targets, c)
	}
	h.mu.Unlock()
	for _, c := range targets {
		c.Send(KindEvent, 0, b)
	}
}

// PublishUser sends an event to a user's connections.
func (h *Hub) PublishUser(userID string, event map[string]any) { h.Publish("user:"+userID, event) }

// Connections counts open WebSockets (metrics).
func (h *Hub) Connections() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.conns)
}

// Close disconnects everyone (server shutdown).
func (h *Hub) Close() {
	h.mu.Lock()
	conns := make([]*Conn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	for _, c := range conns {
		c.shutdown(websocket.StatusGoingAway, "server restarting")
	}
}
