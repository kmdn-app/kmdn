package app

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/audit"
	"github.com/kmdn-app/kmdn/internal/jobs"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/users"
)

func TestAuditLog(t *testing.T) {
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
	sam, samC := mk("sam@northwind.dev", "Sam", access.Contributor)
	tom, tomC := mk("tom@northwind.dev", "Tom", access.Maintainer)
	a.Collab.Options.QuietPeriod = 0

	// Revision events are audited with the repo.
	_, rev := samC.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Better start", "path": "docs/index.md"})
	revID := rev["id"].(string)
	rv, _ := revisions.Get(ctx, a.DB, revID)
	repo, _ := repos.Get(ctx, a.DB, repoID)
	if err := a.Collab.Apply(ctx, repo, rv, revisions.Caller{User: sam, Role: access.Contributor}, "docs/index.md", "# Handbook\n\nStart here, please.\n", "human"); err != nil {
		t.Fatal(err)
	}
	a.Collab.Flush(ctx)
	if code, body := samC.do("POST", "/revisions/"+revID+"/submit", map[string]any{"reviewers": []string{tom.ID}}); code != 200 {
		t.Fatalf("submit: %d %v", code, body)
	}
	tomC.do("POST", "/revisions/"+revID+"/approve", nil)
	// Admin changes: groups and webhooks.
	_, g := admin.do("POST", "/admin/groups", map[string]string{"name": "Docs team"})
	admin.do("PUT", "/admin/groups/"+g["id"].(string)+"/members/"+sam.ID, nil)
	admin.do("DELETE", "/admin/groups/"+g["id"].(string)+"/members/"+sam.ID, nil)
	a.Hooks.AllowPrivate = true
	if code, h := admin.do("POST", "/repos/"+repoID+"/hooks", map[string]any{"kind": "generic", "url": "https://hooks.northwind.dev/kmdn/secret-path", "events": []string{"revision.published"}}); code != 201 {
		t.Fatalf("hook: %d %v", code, h)
	}

	if code, _ := samC.do("GET", "/admin/audit", nil); code != 403 {
		t.Fatalf("non-admin reads the audit log: %d", code)
	}
	_, all := admin.do("GET", "/admin/audit", nil)
	actions := toJSON(all["actions"])
	for _, want := range []string{"revision.submitted", "revision.approved", "group.member_added", "group.member_removed", "hook.created", "repo.connected"} {
		if !strings.Contains(actions, `"`+want+`"`) {
			t.Fatalf("%s not audited: %s", want, actions)
		}
	}
	if strings.Contains(toJSON(all), "secret-path") {
		t.Fatal("a webhook URL's path is in the audit log")
	}

	// Filters: an action family, an actor, a repo, dates.
	_, fam := admin.do("GET", "/admin/audit?action=revision.", nil)
	for _, e := range fam["items"].([]any) {
		m := e.(map[string]any)
		if !strings.HasPrefix(m["action"].(string), "revision.") || m["repo_id"] != repoID || m["repo_name"] == "" {
			t.Fatalf("family filter: %v", m)
		}
	}
	if len(fam["items"].([]any)) < 2 {
		t.Fatalf("revision events: %v", fam)
	}
	_, byTom := admin.do("GET", "/admin/audit?actor_type=user&actor_id="+tom.ID, nil)
	if items := byTom["items"].([]any); len(items) == 0 || items[0].(map[string]any)["actor_name"] != "Tom" {
		t.Fatalf("actor filter: %v", byTom)
	}
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format(time.DateOnly)
	if _, later := admin.do("GET", "/admin/audit?from="+tomorrow, nil); len(later["items"].([]any)) != 0 {
		t.Fatalf("from filter: %v", later)
	}
	if code, _ := admin.do("GET", "/admin/audit?from=yesterday", nil); code != 422 {
		t.Fatalf("bad date: %d", code)
	}

	// Paging with a cursor.
	for i := 0; i < 130; i++ {
		_ = audit.Write(ctx, a.DB, audit.Entry{ActorType: audit.ActorSystem, Action: "test.filler"})
	}
	_, p1 := admin.do("GET", "/admin/audit?action=test.filler", nil)
	cursor, _ := p1["next_cursor"].(string)
	if len(p1["items"].([]any)) != 100 || cursor == "" {
		t.Fatalf("page 1: %d %v", len(p1["items"].([]any)), p1["next_cursor"])
	}
	_, p2 := admin.do("GET", "/admin/audit?action=test.filler&cursor="+cursor, nil)
	if len(p2["items"].([]any)) != 30 || p2["next_cursor"] != nil || p2["items"].([]any)[0].(map[string]any)["id"] == p1["items"].([]any)[99].(map[string]any)["id"] {
		t.Fatalf("page 2: %d %v", len(p2["items"].([]any)), p2["next_cursor"])
	}

	// Exports stream everything that matches.
	get := func(q string) *http.Response {
		req, _ := http.NewRequest("GET", admin.base+"/api/v1/admin/audit/export?"+q, nil)
		res, err := admin.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := get("format=csv&action=test.filler")
	rows, err := csv.NewReader(res.Body).ReadAll()
	res.Body.Close()
	if err != nil || len(rows) != 131 || rows[0][6] != "action" || !strings.HasSuffix(res.Header.Get("Content-Disposition"), `.csv"`) {
		t.Fatalf("csv: %d rows %v %v", len(rows), err, res.Header)
	}
	res = get("format=ndjson&action=revision.")
	sc := bufio.NewScanner(res.Body)
	n := 0
	for sc.Scan() {
		var e map[string]any
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil || !strings.HasPrefix(e["action"].(string), "revision.") {
			t.Fatalf("ndjson line: %s", sc.Text())
		}
		n++
	}
	res.Body.Close()
	if n != len(fam["items"].([]any)) {
		t.Fatalf("ndjson: %d lines, want %d", n, len(fam["items"].([]any)))
	}
	if _, after := admin.do("GET", "/admin/audit?action=audit.exported", nil); len(after["items"].([]any)) != 2 {
		t.Fatalf("exports not audited: %v", after)
	}
}

func TestAdminSystem(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	a.Jobs.Register("test.flaky", func(context.Context, jobs.Job) (any, error) {
		return nil, jobs.Permanent(errors.New("the forge said no"))
	})
	if _, err := a.Jobs.Enqueue(ctx, a.DB, "test.flaky", nil, jobs.EnqueueOptions{Key: "k"}); err != nil {
		t.Fatal(err)
	}
	runJobs(t, a)
	_, sys := admin.do("GET", "/admin/system", nil)
	failed := sys["failed_jobs"].([]any)
	if sys["db"] != "sqlite" || len(failed) != 1 || failed[0].(map[string]any)["last_error"] != "the forge said no" {
		t.Fatalf("system: %v", sys)
	}
	id := failed[0].(map[string]any)["id"].(string)
	if code, _ := admin.do("POST", "/admin/jobs/"+id+"/retry", nil); code != 204 {
		t.Fatalf("retry: %d", code)
	}
	if code, _ := admin.do("POST", "/admin/jobs/"+id+"/retry", nil); code != 409 {
		t.Fatalf("retry twice: %d", code)
	}
	_, sys = admin.do("GET", "/admin/system", nil)
	if sys["jobs"].(map[string]any)["test.flaky"].(map[string]any)["pending"] != float64(1) {
		t.Fatalf("not requeued: %v", sys["jobs"])
	}
	_, doc := admin.do("GET", "/admin/system/doctor", nil)
	if s := toJSON(doc); !strings.Contains(s, `"name":"git"`) || !strings.Contains(s, `"name":"secret_key"`) {
		t.Fatalf("doctor: %s", s)
	}
}
