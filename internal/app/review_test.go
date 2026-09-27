package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/collab"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/users"
)

func states(v map[string]any) string {
	var out []string
	for _, r := range v["reviewers"].([]any) {
		m := r.(map[string]any)
		out = append(out, m["name"].(string)+":"+m["state"].(string))
	}
	return v["state"].(string) + " " + strings.Join(out, ",")
}

func TestReviewLifecycle(t *testing.T) {
	a, admin := newApp(t, nil)
	a.Collab.Options = collab.Options{FlushDelay: time.Millisecond, QuietPeriod: time.Millisecond}
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
	una, unaC := mk("una@northwind.dev", "Una", access.Maintainer)
	_, rev := samC.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Better start", "path": "docs/index.md"})
	revID := rev["id"].(string)
	repo, _ := repos.Get(ctx, a.DB, repoID)
	edit := func(u users.User, role access.Role, md string) {
		t.Helper()
		r, _ := revisions.Get(ctx, a.DB, revID)
		if err := a.Collab.Apply(ctx, repo, r, revisions.Caller{User: u, Role: role}, "docs/index.md", md, "human"); err != nil {
			t.Fatal(err)
		}
		a.Collab.Flush(ctx)
	}
	edit(sam, access.Contributor, "# Handbook\n\nStart here, please.\n")
	// The source diff against the base.
	if _, d := samC.do("GET", "/revisions/"+revID+"/diff/docs/index.md", nil); !strings.Contains(toJSON(d["hunks"]), `{"old":3,"op":"-","text":"Start here."}`) || !strings.Contains(toJSON(d["hunks"]), `{"new":3,"op":"+","text":"Start here, please."}`) {
		t.Fatalf("diff: %v", d)
	}

	// Suggestions: maintainers, not the editor.
	_, sug := samC.do("GET", "/revisions/"+revID+"/reviewer-suggestions", nil)
	if got := toJSON(sug); !strings.Contains(got, tom.ID) || !strings.Contains(got, una.ID) || strings.Contains(got, sam.ID) {
		t.Fatalf("suggestions: %s", got)
	}
	// Submitting needs maintainers as reviewers.
	if code, _ := samC.do("POST", "/revisions/"+revID+"/submit", map[string]any{"reviewers": []string{}}); code != 422 {
		t.Fatalf("no reviewers: %d", code)
	}
	if code, _ := samC.do("POST", "/revisions/"+revID+"/submit", map[string]any{"reviewers": []string{sam.ID}}); code != 422 {
		t.Fatalf("contributor as reviewer: %d", code)
	}
	code, r := samC.do("POST", "/revisions/"+revID+"/submit", map[string]any{"reviewers": []string{tom.ID, una.ID}})
	if code != 200 || states(r) != "in_review Tom:pending,Una:pending" || r["review_round"] != float64(1) {
		t.Fatalf("submit: %d %v", code, r)
	}
	if acc := r["access"].(map[string]any); acc["can_edit"] != false || acc["can_withdraw"] != true || acc["reason"] != "in_review" {
		t.Fatalf("editor access in review: %v", acc)
	}
	_, cps := samC.do("GET", "/revisions/"+revID+"/checkpoints", nil)
	if !strings.Contains(toJSON(cps), `"kind":"submit"`) {
		t.Fatalf("submit checkpoint: %v", cps)
	}
	// Editors are read-only in review; reviewers edit.
	if code, _ := samC.do("POST", "/revisions/"+revID+"/files", map[string]any{"op": "add", "path": "docs/new.md"}); code != 403 {
		t.Fatalf("editor file op in review: %d", code)
	}
	if code, _ := unaC.do("GET", "/revisions/"+revID, nil); code != 200 {
		t.Fatalf("reviewer view: %d", code)
	}
	// Approvals: 1 of 2, then approved.
	if code, r := tomC.do("POST", "/revisions/"+revID+"/approve", nil); code != 200 || states(r) != "in_review Tom:approved,Una:pending" {
		t.Fatalf("tom approves: %d %v", code, r)
	}
	// "Waiting for your review" (the repo home): Una still owes a review;
	// Tom has approved; the editor was never asked.
	waiting := func(c *tc) int {
		_, l := c.do("GET", "/repos/"+repoID+"/revisions?reviewing=true", nil)
		return len(l["items"].([]any))
	}
	if u, tm, sm := waiting(unaC), waiting(tomC), waiting(samC); u != 1 || tm != 0 || sm != 0 {
		t.Fatalf("waiting for review: una %d tom %d sam %d", u, tm, sm)
	}
	if code, r := unaC.do("POST", "/revisions/"+revID+"/approve", nil); code != 200 || states(r) != "approved Tom:approved,Una:approved" {
		t.Fatalf("una approves: %d %v", code, r)
	}
	if code, _ := samC.do("POST", "/revisions/"+revID+"/approve", nil); code != 403 {
		t.Fatalf("editor approves: %d", code)
	}
	// A reviewer's edit resets the other reviewer's approval only.
	edit(tom, access.Maintainer, "# Handbook\n\nStart here, please. Thanks!\n")
	_, r = samC.do("GET", "/revisions/"+revID, nil)
	if states(r) != "in_review Tom:approved,Una:pending" {
		t.Fatalf("after reviewer edit: %s", states(r))
	}
	// Changes requested: back to editing with the flag.
	code, r = unaC.do("POST", "/revisions/"+revID+"/request-changes", map[string]any{"note": "Add a link to the setup guide."})
	if code != 200 || r["state"] != "editing" || r["changes_requested"] != true {
		t.Fatalf("request changes: %d %v", code, r)
	}
	if code, _ := samC.do("POST", "/revisions/"+revID+"/files", map[string]any{"op": "add", "path": "docs/setup.md"}); code != 200 {
		t.Fatalf("editor edits again: %d", code)
	}
	// Resubmitting starts a new round: nobody has approved it yet.
	code, r = samC.do("POST", "/revisions/"+revID+"/submit", map[string]any{"reviewers": []string{tom.ID, una.ID}})
	if code != 200 || states(r) != "in_review Tom:pending,Una:pending" || r["review_round"] != float64(2) || r["changes_requested"] != false {
		t.Fatalf("resubmit: %d %v", code, r)
	}
	// Removing the last reviewer returns it to editing; withdraw works too.
	samC.do("DELETE", "/revisions/"+revID+"/reviewers/"+una.ID, nil)
	if code, r := tomC.do("POST", "/revisions/"+revID+"/approve", nil); code != 200 || states(r) != "approved Tom:approved" {
		t.Fatalf("approve with one reviewer left: %d %v", code, r)
	}
	if code, r := samC.do("POST", "/revisions/"+revID+"/withdraw", nil); code != 200 || r["state"] != "editing" {
		t.Fatalf("withdraw: %d %v", code, r)
	}
	_, ev := samC.do("GET", "/revisions/"+revID+"/events", nil)
	var kinds []string
	for _, e := range ev["items"].([]any) {
		kinds = append(kinds, e.(map[string]any)["kind"].(string))
	}
	want := "created,submitted,approval,approval,approved,approvals_reset,in_review_again,changes_requested,file_added,submitted,reviewer_removed,approval,approved,withdrawn"
	if got := strings.Join(kinds, ","); got != want {
		t.Fatalf("events:\n%s\nwant\n%s", got, want)
	}
}
