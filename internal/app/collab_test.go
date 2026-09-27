package app

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/collab"
	"github.com/kmdn-app/kmdn/internal/realtime"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

type wsc struct {
	t  *testing.T
	ws *websocket.Conn
}

type frame struct {
	kind    byte
	channel uint32
	payload []byte
}

func dialWS(t *testing.T, a *App, c *tc) *wsc {
	t.Helper()
	h := http.Header{}
	h.Set("Origin", a.Realtime.Origin)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, strings.Replace(c.base, "http", "ws", 1)+"/ws", &websocket.DialOptions{HTTPClient: c.c, HTTPHeader: h})
	if err != nil {
		t.Fatal(err)
	}
	ws.SetReadLimit(4 << 20)
	t.Cleanup(func() { _ = ws.CloseNow() })
	return &wsc{t: t, ws: ws}
}

func (w *wsc) send(kind byte, channel uint32, payload []byte) {
	w.t.Helper()
	msg := make([]byte, 5+len(payload))
	msg[0] = kind
	binary.BigEndian.PutUint32(msg[1:5], channel)
	copy(msg[5:], payload)
	if err := w.ws.Write(context.Background(), websocket.MessageBinary, msg); err != nil {
		w.t.Fatal(err)
	}
}

func (w *wsc) control(v any) {
	w.t.Helper()
	b, _ := json.Marshal(v)
	w.send(realtime.KindControl, 0, b)
}

// next reads frames until match accepts one.
func (w *wsc) next(what string, match func(frame) bool) frame {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, b, err := w.ws.Read(ctx)
		if err != nil {
			w.t.Fatalf("waiting for %s: %v", what, err)
		}
		f := frame{kind: b[0], channel: binary.BigEndian.Uint32(b[1:5]), payload: b[5:]}
		if match(f) {
			return f
		}
	}
}

func (w *wsc) op(op string) map[string]any {
	w.t.Helper()
	var out map[string]any
	w.next("control "+op, func(f frame) bool {
		if f.kind != realtime.KindControl {
			return false
		}
		out = map[string]any{}
		_ = json.Unmarshal(f.payload, &out)
		return out["op"] == op
	})
	return out
}

func (w *wsc) syncOf(typ byte) []byte {
	w.t.Helper()
	f := w.next("sync", func(f frame) bool { return f.kind == realtime.KindSync && f.payload[0] == typ })
	return varBytes(f.payload[1:])
}

func varBytes(b []byte) []byte {
	n, k := binary.Uvarint(b)
	return b[k : k+int(n)]
}

func syncFrame(typ byte, data []byte) []byte {
	out := []byte{typ}
	out = binary.AppendUvarint(out, uint64(len(data)))
	return append(out, data...)
}

func awareness(client, clock uint64, state string) []byte {
	b := binary.AppendUvarint(nil, 1)
	b = binary.AppendUvarint(b, client)
	b = binary.AppendUvarint(b, clock)
	b = binary.AppendUvarint(b, uint64(len(state)))
	return append(b, state...)
}

func TestCollaborativeEditingOverWebSocket(t *testing.T) {
	a, admin := newApp(t, nil)
	// A quiet period shorter than adding the page to the manifest exercises that race.
	a.Collab.Options = collab.Options{FlushDelay: time.Millisecond, QuietPeriod: time.Millisecond}
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	repoID := connectLocal(t, a, admin, map[string]string{"docs/index.md": "# Handbook\n\nStart   here.\n\nKeep this.\n"})
	sam, _ := users.Create(ctx, a.DB, "sam@northwind.dev", "Sam", false)
	vic, _ := users.Create(ctx, a.DB, "vic@northwind.dev", "Vic", false)
	_ = access.Grant(ctx, a.DB, repoID, "user", sam.ID, access.Contributor)
	_ = access.Grant(ctx, a.DB, repoID, "user", vic.ID, access.Viewer)
	samC := &tc{t: t, base: admin.base, c: newClient()}
	signIn(t, a, samC, sam)
	vicC := &tc{t: t, base: admin.base, c: newClient()}
	signIn(t, a, vicC, vic)
	_, rev := samC.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Tweaks"})
	revID := rev["id"].(string)
	room := map[string]any{"revision": revID, "path": "docs/index.md"}

	// Cross-origin upgrades are refused.
	h := http.Header{}
	h.Set("Origin", "https://evil.example")
	if _, _, err := websocket.Dial(ctx, strings.Replace(samC.base, "http", "ws", 1)+"/ws", &websocket.DialOptions{HTTPClient: samC.c, HTTPHeader: h}); err == nil {
		t.Fatal("cross-origin dial succeeded")
	}

	// A viewer can't create the document by opening an untouched page.
	vw := dialWS(t, a, vicC)
	vw.control(map[string]any{"op": "subscribe", "channel": 3, "room": room})
	if e := vw.op("error"); e["code"] != "no_document" {
		t.Fatalf("viewer first open: %v", e)
	}

	// Sam opens the page: the server creates the document from the base.
	sw := dialWS(t, a, samC)
	sw.control(map[string]any{"op": "subscribe", "channel": 1, "room": room})
	if s := sw.op("subscribed"); s["mode"] != "rw" {
		t.Fatalf("subscribed: %v", s)
	}
	sw.syncOf(0)                                           // server step 1
	sw.send(realtime.KindSync, 1, syncFrame(0, []byte{0})) // our step 1: empty state vector
	state := sw.syncOf(1)                                  // server step 2: the whole document
	if md, err := a.Engine.YMaterialize(ctx, state, ""); err != nil || !strings.Contains(md, "Start   here.") {
		t.Fatalf("initial state: %v %q", err, md)
	}
	// Opening a page doesn't add it to the revision.
	if _, files := samC.do("GET", "/revisions/"+revID+"/files", nil); len(files["items"].([]any)) != 0 {
		t.Fatalf("manifest before any edit: %v", files)
	}

	// Revision-wide presence: Sam, editing docs/index.md.
	if _, pr := samC.do("GET", "/revisions/"+revID+"/presence", nil); toJSON(pr["items"]) != `[{"editing":true,"id":"`+sam.ID+`","name":"Sam","paths":["docs/index.md"]}]` {
		t.Fatalf("presence: %v", pr)
	}
	vw.control(map[string]any{"op": "subscribe-events", "scope": "revision:" + revID})
	vw.op("events-subscribed")

	// Now the viewer can follow along, read-only.
	vw.control(map[string]any{"op": "subscribe", "channel": 4, "room": room})
	if s := vw.op("subscribed"); s["mode"] != "ro" || s["reason"] != "viewer" {
		t.Fatalf("viewer subscribed: %v", s)
	}
	vw.next("presence event with both", func(f frame) bool {
		return f.kind == realtime.KindEvent && strings.Contains(string(f.payload), `"type":"presence"`) && strings.Contains(string(f.payload), `"Vic"`) && strings.Contains(string(f.payload), `"Sam"`)
	})
	sw.send(realtime.KindAwareness, 1, awareness(4242, 1, `{"user":{"name":"Sam"}}`))
	f := vw.next("awareness", func(f frame) bool {
		return f.kind == realtime.KindAwareness && strings.Contains(string(f.payload), "Sam")
	})
	if f.channel != 4 {
		t.Fatalf("awareness on channel %d", f.channel)
	}

	// Sam edits; the viewer receives the update.
	edit, err := a.Engine.YApplyMarkdown(ctx, state, "# Handbook\n\nStart   here.\n\nKeep this, please.\n", 777)
	if err != nil {
		t.Fatal(err)
	}
	sw.send(realtime.KindSync, 1, syncFrame(2, edit))
	if got := vw.syncOf(2); string(got) != string(edit) {
		t.Fatal("viewer got a different update")
	}
	// The viewer's own edits are refused.
	other, _ := a.Engine.YApplyMarkdown(ctx, state, "# Vandalized\n", 888)
	vw.send(realtime.KindSync, 4, syncFrame(2, other))
	if e := vw.op("error"); e["code"] != "read_only" {
		t.Fatalf("viewer edit: %v", e)
	}

	// After the quiet period the page is in the revision with its new content;
	// untouched bytes (the odd spacing) are kept.
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, c := samC.do("GET", "/revisions/"+revID+"/files/docs/index.md", nil)
		if code == 200 && c["content"] == "# Handbook\n\nStart   here.\n\nKeep this, please.\n" && c["op"] == "modify" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("materialized content: %d %v", code, c)
		}
		time.Sleep(20 * time.Millisecond)
	}
	var owner string
	if err := store.QueryRow(ctx, a.DB, `SELECT c.user_id FROM ydoc_clients c JOIN ydocs d ON d.id = c.ydoc_id WHERE d.revision_id = ? AND c.client_id = 777`, revID).Scan(&owner); err != nil || owner != sam.ID {
		t.Fatalf("client attribution: %q %v", owner, err)
	}

	// Leaving clears presence for the others.
	_ = sw.ws.Close(websocket.StatusNormalClosure, "")
	vw.next("presence removal", func(f frame) bool {
		return f.kind == realtime.KindAwareness && strings.Contains(string(f.payload), "null")
	})

	// A fresh hub (as after a restart) rebuilds the document from the log.
	a.Collab.Flush(ctx)
	fresh := &collab.Hub{DB: a.DB, Engine: a.Engine, Revisions: a.Revisions, Log: a.Log}
	a.Realtime.Rooms = fresh
	a.Collab = fresh
	sw2 := dialWS(t, a, samC)
	sw2.control(map[string]any{"op": "subscribe", "channel": 1, "room": room})
	sw2.op("subscribed")
	sw2.send(realtime.KindSync, 1, syncFrame(0, []byte{0}))
	state2 := sw2.syncOf(1)
	if md, _ := a.Engine.YMaterialize(ctx, state2, ""); !strings.Contains(md, "Keep this, please.") {
		t.Fatalf("after reload: %q", md)
	}

	// Closing the revision switches open editors to read-only.
	samC.do("POST", "/revisions/"+revID+"/close", nil)
	if m := sw2.op("mode"); m["mode"] != "ro" || m["reason"] != "closed" {
		t.Fatalf("mode switch: %v", m)
	}
}
