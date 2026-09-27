package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/hooks"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

type hookCall struct {
	path    string
	headers http.Header
	body    []byte
}

func TestWebhooks(t *testing.T) {
	var mu sync.Mutex
	var calls []hookCall
	fail := 1 // the first generic delivery fails
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, hookCall{r.URL.Path, r.Header.Clone(), b})
		failNow := r.URL.Path == "/generic" && fail > 0
		if failNow {
			fail--
		}
		mu.Unlock()
		if failNow {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	a, admin := newApp(t, nil)
	a.Hooks.AllowPrivate = true // the test server is on loopback
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
	sam, samC := mk("sam@northwind.dev", "Sam", access.Contributor)
	tom, _ := mk("tom@northwind.dev", "Tom", access.Maintainer)
	_, luisC := mk("luis@northwind.dev", "Luis", access.Viewer)
	a.Collab.Options.QuietPeriod = 0

	if code, _ := samC.do("GET", "/repos/"+repoID+"/hooks", nil); code != 403 {
		t.Fatalf("contributor lists hooks: %d", code)
	}
	if code, _ := admin.do("POST", "/repos/"+repoID+"/hooks", map[string]any{"kind": "generic", "url": srv.URL + "/generic", "events": []string{"revision.nope"}}); code != 422 {
		t.Fatalf("unknown event: %d", code)
	}
	if code, _ := admin.do("POST", "/repos/"+repoID+"/hooks", map[string]any{"kind": "slack", "url": srv.URL + "/slack", "events": []string{"revision.submitted"}}); code != 422 {
		t.Fatalf("slack over http: %d", code)
	}
	code, created := admin.do("POST", "/repos/"+repoID+"/hooks", map[string]any{"kind": "generic", "url": srv.URL + "/generic", "events": []string{"revision.submitted", "discussion.created"}})
	if code != 201 || !strings.HasPrefix(created["secret"].(string), "whsec_") {
		t.Fatalf("create: %d %v", code, created)
	}
	secret := created["secret"].(string)
	hookID := created["hook"].(map[string]any)["id"].(string)
	// Slack hooks need https: point one at the test server through the store directly.
	sl, _, err := a.Hooks.Create(ctx, repoID, maya.ID, hooks.Input{Kind: hooks.KindGeneric, URL: srv.URL + "/slack", Events: []string{"revision.submitted"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Exec(ctx, a.DB, `UPDATE repo_hooks SET kind = 'slack', secret_ref = '' WHERE id = ?`, sl.ID); err != nil {
		t.Fatal(err)
	}

	_, rev := samC.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Start", "path": "docs/index.md"})
	revID := rev["id"].(string)
	rv, _ := revisions.Get(ctx, a.DB, revID)
	repo, _ := repos.Get(ctx, a.DB, repoID)
	if err := a.Collab.Apply(ctx, repo, rv, revisions.Caller{User: sam, Role: access.Contributor}, "docs/index.md", "# Handbook\n\nStart here, now.\n", "human"); err != nil {
		t.Fatal(err)
	}
	a.Collab.FlushRevision(ctx, revID)
	samC.do("POST", "/revisions/"+revID+"/submit", map[string]any{"reviewers": []string{tom.ID}})
	if err := a.Notify.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	runJobs(t, a)
	// The generic hook failed once: its retry is scheduled; run it now.
	if _, err := store.Exec(ctx, a.DB, `UPDATE jobs SET run_at = 0 WHERE kind = ? AND status = 'pending'`, hooks.JobDeliver); err != nil {
		t.Fatal(err)
	}
	runJobs(t, a)

	mu.Lock()
	got := append([]hookCall(nil), calls...)
	mu.Unlock()
	var generic, slack []hookCall
	for _, c := range got {
		if c.path == "/generic" {
			generic = append(generic, c)
		} else {
			slack = append(slack, c)
		}
	}
	if len(generic) != 2 || len(slack) != 1 {
		t.Fatalf("calls: %d generic, %d slack", len(generic), len(slack))
	}
	g := generic[1]
	if g.headers.Get("X-Kmdn-Event") != "revision.submitted" || g.headers.Get("X-Kmdn-Signature") != hooks.Sign([]byte(secret), g.body) || g.headers.Get("X-Kmdn-Delivery") != generic[0].headers.Get("X-Kmdn-Delivery") {
		t.Fatalf("generic headers: %v", g.headers)
	}
	var p hooks.Payload
	if err := json.Unmarshal(g.body, &p); err != nil || p.Type != "revision.submitted" || p.Revision["title"] != "Start" || p.Actor["name"] != "Sam" || p.Repo["slug"] == "" {
		t.Fatalf("payload: %s", g.body)
	}
	if !strings.Contains(string(slack[0].body), `"blocks"`) || !strings.Contains(string(slack[0].body), "Sam asked for review") {
		t.Fatalf("slack: %s", slack[0].body)
	}
	_, ds := admin.do("GET", "/repos/"+repoID+"/hooks/"+hookID+"/deliveries", nil)
	d0 := ds["items"].([]any)[0].(map[string]any)
	if d0["status"] != "delivered" || d0["attempts"] != float64(2) || d0["response_code"] != float64(200) {
		t.Fatalf("delivery log: %v", d0)
	}

	// A discussion on a published page, and a redelivery.
	luisC.do("POST", "/repos/"+repoID+"/discussions", map[string]any{"path": "docs/index.md", "anchor": map[string]any{"quote": "Start here."}, "body": "Where?"})
	if code, _ := admin.do("POST", "/repos/"+repoID+"/hooks/"+hookID+"/deliveries/"+d0["id"].(string)+"/redeliver", nil); code != 202 {
		t.Fatalf("redeliver: %d", code)
	}
	runJobs(t, a)
	mu.Lock()
	events := []string{}
	for _, c := range calls[len(got):] {
		events = append(events, c.headers.Get("X-Kmdn-Event"))
	}
	mu.Unlock()
	if strings.Join(events, ",") != "discussion.created,revision.submitted" {
		t.Fatalf("later events: %v", events)
	}

	// Without AllowPrivate, loopback is refused.
	a.Hooks.AllowPrivate = false
	admin.do("POST", "/repos/"+repoID+"/hooks/"+hookID+"/ping", nil)
	runJobs(t, a)
	_, ds = admin.do("GET", "/repos/"+repoID+"/hooks/"+hookID+"/deliveries", nil)
	if last := ds["items"].([]any)[0].(map[string]any); last["event"] != "ping" || !strings.Contains(last["error"].(string), "private network") {
		t.Fatalf("private address: %v", last)
	}
}
