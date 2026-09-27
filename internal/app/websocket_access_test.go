package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/groups"
	"github.com/kmdn-app/kmdn/internal/realtime"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

// A pong follows previously received updates on this connection. Closing a
// revoked session also proves that it cannot receive more document data.
func settleAccessSocket(t *testing.T, w *wsc, forbidData bool) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	msg := append(make([]byte, 5), []byte(`{"op":"ping"}`)...)
	if err := w.ws.Write(ctx, websocket.MessageBinary, msg); err != nil {
		return true
	}
	for {
		_, b, err := w.ws.Read(ctx)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("revoked socket did not settle")
			}
			return true
		}
		if len(b) < 5 {
			continue
		}
		if forbidData && ((b[0] == realtime.KindSync && len(b) > 5 && b[5] == 2) || strings.Contains(string(b[5:]), "PRIVATE-EVENT")) {
			t.Fatal("revoked socket received new private content")
		}
		if b[0] == realtime.KindControl {
			var c map[string]any
			_ = json.Unmarshal(b[5:], &c)
			if c["op"] == "pong" {
				return false
			}
		}
	}
}

func TestWebSocketRechecksLiveAccess(t *testing.T) {
	for _, change := range []string{"direct", "group", "deactivated", "session", "expired", "admin", "viewer"} {
		t.Run(change, func(t *testing.T) {
			a, admin := newApp(t, nil)
			ctx := context.Background()
			owner, err := users.Create(ctx, a.DB, "owner@example.org", "Owner", true)
			if err != nil {
				t.Fatal(err)
			}
			signIn(t, a, admin, owner)
			repo := connectLocal(t, a, admin, map[string]string{"docs/index.md": "# Handbook\n\nOriginal.\n"})
			u, err := users.Create(ctx, a.DB, "editor@example.org", "Editor", change == "admin")
			if err != nil {
				t.Fatal(err)
			}
			var groupID string
			if change == "group" {
				g, err := groups.Create(ctx, a.DB, "Editors", "")
				if err != nil {
					t.Fatal(err)
				}
				groupID = g.ID
				if err := groups.AddMember(ctx, a.DB, g.ID, u.ID); err != nil {
					t.Fatal(err)
				}
				if err := access.Grant(ctx, a.DB, repo, access.GroupPrincipal, g.ID, access.Contributor); err != nil {
					t.Fatal(err)
				}
			} else if change != "admin" {
				if err := access.Grant(ctx, a.DB, repo, access.UserPrincipal, u.ID, access.Contributor); err != nil {
					t.Fatal(err)
				}
			}
			editor := &tc{t: t, base: admin.base, c: newClient()}
			signIn(t, a, editor, u)
			code, rev := editor.do("POST", "/repos/"+repo+"/revisions", map[string]any{"title": "Access test"})
			if code != 201 {
				t.Fatalf("revision: %d %v", code, rev)
			}
			rid := rev["id"].(string)
			room := map[string]any{"revision": rid, "path": "docs/index.md"}
			ew := dialWS(t, a, editor)
			ew.control(map[string]any{"op": "subscribe", "channel": 1, "room": room})
			if got := ew.op("subscribed"); got["mode"] != "rw" {
				t.Fatalf("mode: %v", got)
			}
			ew.syncOf(0)
			ew.send(realtime.KindSync, 1, syncFrame(0, []byte{0}))
			state := ew.syncOf(1)
			ew.control(map[string]any{"op": "subscribe-events", "scope": "repo:" + repo})
			ew.op("events-subscribed")
			aw := dialWS(t, a, admin)
			aw.control(map[string]any{"op": "subscribe", "channel": 2, "room": room})
			aw.op("subscribed")
			aw.syncOf(0)
			settleAccessSocket(t, ew, false)
			switch change {
			case "group":
				err = groups.RemoveMember(ctx, a.DB, groupID, u.ID)
			case "deactivated":
				_, err = store.Exec(ctx, a.DB, `UPDATE users SET status = 'deactivated' WHERE id = ?`, u.ID)
			case "session":
				err = a.Auth.RevokeUser(ctx, u.ID, "")
			case "expired":
				_, err = store.Exec(ctx, a.DB, `UPDATE sessions SET expires_at = ? WHERE user_id = ?`, store.Millis(time.Now().Add(-time.Minute)), u.ID)
			case "admin":
				_, err = store.Exec(ctx, a.DB, `UPDATE users SET is_instance_admin = FALSE WHERE id = ?`, u.ID)
			case "viewer":
				err = access.Grant(ctx, a.DB, repo, access.UserPrincipal, u.ID, access.Viewer)
			default:
				err = access.Revoke(ctx, a.DB, repo, access.UserPrincipal, u.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			bad, err := a.Engine.YApplyMarkdown(ctx, state, "# Unauthorized tampering\n", 777)
			if err != nil {
				t.Fatal(err)
			}
			ew.send(realtime.KindSync, 1, syncFrame(2, bad))
			closed := settleAccessSocket(t, ew, false)
			aw.send(realtime.KindSync, 2, syncFrame(0, []byte{0}))
			current := aw.syncOf(1)
			md, err := a.Engine.YMaterialize(ctx, current, "")
			if err != nil || strings.Contains(md, "Unauthorized") {
				t.Fatalf("revoked write took effect: %q, %v", md, err)
			}
			good, err := a.Engine.YApplyMarkdown(ctx, current, "# Authorized update\n", 888)
			if err != nil {
				t.Fatal(err)
			}
			aw.send(realtime.KindSync, 2, syncFrame(2, good))
			settleAccessSocket(t, aw, false)
			a.Realtime.Publish("repo:"+repo, map[string]any{"type": "PRIVATE-EVENT"})
			if !closed {
				if change == "viewer" {
					if got := ew.syncOf(2); string(got) != string(good) {
						t.Fatal("viewer lost authorized read access")
					}
				} else {
					settleAccessSocket(t, ew, true)
					ew.control(map[string]any{"op": "subscribe", "channel": 3, "room": room})
					if e := ew.op("error"); e["code"] != "not_found" {
						t.Fatalf("new subscription survived revocation: %v", e)
					}
				}
			}
		})
	}
}
