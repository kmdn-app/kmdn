package app

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/notify"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/users"
)

func TestNotifications(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	repoID := connectLocal(t, a, admin, map[string]string{"docs/index.md": "# Handbook\n\nStart here.\n"})
	mk := func(email, name string, role access.Role) (users.User, *tc) {
		u, _ := users.Create(ctx, a.DB, email, name, false)
		_ = access.Grant(ctx, a.DB, repoID, "user", u.ID, role)
		c := &tc{t: t, base: admin.base, c: newClient()}
		signIn(t, a, c, u)
		return u, c
	}
	sam, samC := mk("sam@northwind.dev", "Sam Lindqvist", access.Contributor)
	tom, tomC := mk("tom.okafor@northwind.dev", "Tom Okafor", access.Maintainer)
	a.Collab.Options.QuietPeriod = 0

	var mu sync.Mutex
	var pushes []notify.Message
	a.Notify.Push.Transport = func(_ context.Context, _ notify.Subscription, payload []byte, keys notify.VAPID) (int, error) {
		var m notify.Message
		_ = json.Unmarshal(payload, &m)
		mu.Lock()
		pushes = append(pushes, m)
		mu.Unlock()
		if keys.Public == "" || keys.Private == "" {
			t.Error("no VAPID keys")
		}
		return 201, nil
	}
	if code, k := tomC.do("GET", "/push/key", nil); code != 200 || k["public_key"] == "" {
		t.Fatalf("push key: %d %v", code, k)
	}
	if code, _ := tomC.do("POST", "/me/push-subscriptions", map[string]any{"endpoint": "http://insecure.example/x", "keys": map[string]string{"p256dh": "k", "auth": "a"}}); code != 422 {
		t.Fatalf("insecure endpoint: %d", code)
	}
	if code, _ := tomC.do("POST", "/me/push-subscriptions", map[string]any{"endpoint": "https://push.example/tom", "keys": map[string]string{"p256dh": "k", "auth": "a"}}); code != 204 {
		t.Fatalf("subscribe: %d", code)
	}

	_, rev := samC.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Start", "path": "docs/index.md"})
	revID := rev["id"].(string)
	rv, _ := revisions.Get(ctx, a.DB, revID)
	repo, _ := repos.Get(ctx, a.DB, repoID)
	if err := a.Collab.Apply(ctx, repo, rv, revisions.Caller{User: sam, Role: access.Contributor}, "docs/index.md", "# Handbook\n\nStart here, now.\n", "human"); err != nil {
		t.Fatal(err)
	}
	a.Collab.FlushRevision(ctx, revID)
	inbox := func(c *tc) ([]map[string]any, float64) {
		t.Helper()
		if err := a.Notify.Drain(ctx); err != nil {
			t.Fatal(err)
		}
		_, body := c.do("GET", "/notifications", nil)
		var out []map[string]any
		for _, it := range body["items"].([]any) {
			out = append(out, it.(map[string]any))
		}
		return out, body["unread"].(float64)
	}
	kinds := func(items []map[string]any) map[string]int {
		k := map[string]int{}
		for _, it := range items {
			k[it["kind"].(string)]++
		}
		return k
	}

	if code, body := samC.do("POST", "/revisions/"+revID+"/submit", map[string]any{"reviewers": []string{tom.ID}}); code != 200 {
		t.Fatalf("submit: %d %v", code, body)
	}
	items, unread := inbox(tomC)
	if kinds(items)["review_requested"] != 1 || unread != 1 {
		t.Fatalf("tom's inbox: %v", items)
	}
	if d := items[0]["data"].(map[string]any); d["title"] != "Start" || d["owner"] == "" || items[0]["actor_name"] != "Sam Lindqvist" {
		t.Fatalf("notification data: %v", items[0])
	}
	if own, _ := inbox(samC); len(own) != 0 {
		t.Fatalf("the actor got notified: %v", own)
	}

	if code, _ := tomC.do("POST", "/revisions/"+revID+"/approve", nil); code != 200 {
		t.Fatalf("approve: %d", code)
	}
	if k := func() map[string]int { it, _ := inbox(samC); return kinds(it) }(); k["approval"] != 1 || k["approved"] != 1 {
		t.Fatalf("sam's inbox: %v", k)
	}

	// Mentions and replies.
	_, th := samC.do("POST", "/revisions/"+revID+"/threads", map[string]any{"path": "docs/index.md", "anchor": map[string]any{"quote": "now"}, "body": "@tom.okafor is this clear?"})
	if k := func() map[string]int { it, _ := inbox(tomC); return kinds(it) }(); k["mention"] != 1 {
		t.Fatalf("mention: %v", k)
	}
	if code, _ := samC.do("PUT", "/me/notification-prefs", map[string]any{"items": []map[string]any{{"kind": "reply", "in_app": false, "push": false}}}); code != 200 {
		t.Fatalf("prefs: %d", code)
	}
	tomC.do("POST", "/threads/"+th["id"].(string)+"/comments", map[string]any{"body": "Yes."})
	if k := func() map[string]int { it, _ := inbox(samC); return kinds(it) }(); k["reply"] != 0 {
		t.Fatalf("reply despite prefs: %v", k)
	}

	// One push per revision per window: the review request went out, the mention didn't.
	a.Notify.Wait()
	mu.Lock()
	got := append([]notify.Message(nil), pushes...)
	mu.Unlock()
	if len(got) != 1 || got[0].Title != "Review requested" || got[0].Body != "Start" {
		t.Fatalf("pushes: %+v", got)
	}

	if code, _ := tomC.do("POST", "/notifications/read", map[string]any{"all": true}); code != 204 {
		t.Fatalf("read: %d", code)
	}
	if _, unread := inbox(tomC); unread != 0 {
		t.Fatalf("unread after read all: %v", unread)
	}
}
