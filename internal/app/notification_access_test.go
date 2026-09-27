package app

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/groups"
	"github.com/kmdn-app/kmdn/internal/notify"
	"github.com/kmdn-app/kmdn/internal/orgs"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

func TestNotificationsRespectCurrentAccess(t *testing.T) {
	for _, change := range []string{"direct", "group", "deactivated"} {
		t.Run(change, func(t *testing.T) {
			a, admin := newApp(t, nil)
			ctx := context.Background()
			owner, err := users.Create(ctx, a.DB, "admin@example.org", "Admin", true)
			if err != nil {
				t.Fatal(err)
			}
			signIn(t, a, admin, owner)
			repo := connectLocal(t, a, admin, map[string]string{"docs/policy.md": "# Policy\n\nOriginal text.\n"})
			u, err := users.Create(ctx, a.DB, "viewer@example.org", "Viewer", false)
			if err != nil {
				t.Fatal(err)
			}
			var groupID string
			if change == "group" {
				group, err := groups.Create(ctx, a.DB, orgs.DefaultID, "Readers", "")
				if err != nil {
					t.Fatal(err)
				}
				groupID = group.ID
				if err := groups.AddMember(ctx, a.DB, groupID, u.ID); err != nil {
					t.Fatal(err)
				}
				if err := access.Grant(ctx, a.DB, repo, access.GroupPrincipal, groupID, access.Viewer); err != nil {
					t.Fatal(err)
				}
			} else if err := access.Grant(ctx, a.DB, repo, access.UserPrincipal, u.ID, access.Viewer); err != nil {
				t.Fatal(err)
			}
			viewer := &tc{t: t, base: admin.base, c: newClient()}
			signIn(t, a, viewer, u)
			var pushes atomic.Int32
			a.Notify.Push.Transport = func(context.Context, notify.Subscription, []byte, notify.VAPID) (int, error) {
				pushes.Add(1)
				return 201, nil
			}
			if err := notify.Subscribe(ctx, a.DB, u.ID, notify.Subscription{Endpoint: "https://push.example.org/viewer", P256dh: "key", Auth: "auth"}, "test"); err != nil {
				t.Fatal(err)
			}
			code, thread := viewer.do("POST", "/repos/"+repo+"/discussions", map[string]any{"path": "docs/policy.md", "anchor": map[string]any{"quote": "Original text."}, "body": "Question"})
			if code != 201 {
				t.Fatalf("discussion: %d %v", code, thread)
			}
			threadID := thread["id"].(string)
			if code, body := admin.do("POST", "/threads/"+threadID+"/comments", map[string]any{"body": "Visible while authorized"}); code != 201 {
				t.Fatalf("reply: %d %v", code, body)
			}
			a.Notify.Wait()
			if pushes.Swap(0) != 1 {
				t.Fatal("authorized viewer did not receive a push")
			}
			switch change {
			case "group":
				if err := groups.RemoveMember(ctx, a.DB, groupID, u.ID); err != nil {
					t.Fatal(err)
				}
			case "deactivated":
				if code, body := admin.do("PATCH", "/admin/users/"+u.ID, map[string]any{"status": "deactivated"}); code != 200 {
					t.Fatalf("deactivate: %d %v", code, body)
				}
			default:
				if code, body := admin.do("DELETE", "/repos/"+repo+"/members/user/"+u.ID, nil); code != 204 {
					t.Fatalf("remove: %d %v", code, body)
				}
			}
			if code, _ := viewer.do("GET", "/threads/"+threadID, nil); code == 200 {
				t.Fatal("removed viewer can still read thread")
			}
			if code, body := admin.do("POST", "/threads/"+threadID+"/comments", map[string]any{"body": "Confidential ALPHA987"}); code != 201 {
				t.Fatalf("reply: %d %v", code, body)
			}
			a.Notify.Wait()
			if pushes.Load() != 0 {
				t.Fatal("removed viewer received a new push")
			}
			items, unread, err := notify.List(ctx, a.DB, u.ID, false, 100)
			if err != nil || len(items) != 0 || unread != 0 {
				t.Fatalf("inaccessible inbox: %v, unread %d, %v", items, unread, err)
			}
			var data string
			if err := store.QueryRow(ctx, a.DB, `SELECT data FROM notifications WHERE user_id = ? ORDER BY created_at DESC LIMIT 1`, u.ID).Scan(&data); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(data, "ALPHA987") {
				t.Fatal("new private content was stored for removed viewer")
			}
		})
	}
}
