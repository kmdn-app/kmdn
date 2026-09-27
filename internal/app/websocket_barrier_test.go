package app

import (
	"context"
	"testing"

	"github.com/kmdn-app/kmdn/internal/users"
)

func TestWebSocketPingCorrelation(t *testing.T) {
	a, client := newApp(t, nil)
	u, err := users.Create(context.Background(), a.DB, "viewer@example.org", "Viewer", false)
	if err != nil {
		t.Fatal(err)
	}
	signIn(t, a, client, u)
	w := dialWS(t, a, client)
	w.control(map[string]any{"op": "ping", "request_id": "flush-123"})
	if reply := w.op("pong"); reply["request_id"] != "flush-123" {
		t.Fatalf("uncorrelated pong: %v", reply)
	}
	w.control(map[string]any{"op": "ping"})
	if reply := w.op("pong"); reply["request_id"] != nil {
		t.Fatalf("ordinary heartbeat changed: %v", reply)
	}
}
