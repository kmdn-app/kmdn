package app

import (
	"context"
	"strings"
	"testing"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/llm"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/users"
)

func TestReviewAssistant(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	repoID := connectLocal(t, a, admin, map[string]string{
		"docs/policy.md": "---\nowner: it\ntags: [equipment]\n---\n# Policy\n\n## Laptops\n\nEvery three years.\n",
		"docs/x.md":      "---\nowner: hr\n---\n# X\n",
		"docs/y.md":      "---\nowner: ops\n---\n# Y\n",
		"STYLE.md":       "# Style\n\nWrite numbers under ten as words.\n",
	})
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
	model := &scripted{}
	a.LLM.Override = model

	_, rev := samC.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Laptops every 4 years", "path": "docs/policy.md"})
	revID := rev["id"].(string)
	rv, _ := revisions.Get(ctx, a.DB, revID)
	repo, _ := repos.Get(ctx, a.DB, repoID)
	edited := "# Policy\n\n## Laptops\n\nEvery 4 years. See [the old policy](missing.md).\n\n![](laptop.png)\n\n#### Exceptions\n\nNone.\n"
	if err := a.Collab.Apply(ctx, repo, rv, revisions.Caller{User: sam, Role: access.Contributor}, "docs/policy.md", edited, "human"); err != nil {
		t.Fatal(err)
	}

	// Checks without AI.
	_, rs := samC.do("GET", "/revisions/"+revID+"/review-summary", nil)
	kinds := map[string][]string{}
	for _, c := range rs["checks"].([]any) {
		m := c.(map[string]any)
		kinds[m["kind"].(string)] = append(kinds[m["kind"].(string)], m["message"].(string))
	}
	if !strings.Contains(strings.Join(kinds["broken_link"], " "), "missing.md") || len(kinds["missing_alt"]) != 1 || len(kinds["heading_jump"]) != 1 {
		t.Fatalf("checks: %v", kinds)
	}
	fm := strings.Join(kinds["frontmatter"], " | ")
	if !strings.Contains(fm, "“tags” is removed") || !strings.Contains(fm, "“owner” is removed") || strings.Contains(fm, "Other pages") {
		t.Fatalf("front matter: %s", fm)
	}
	if rs["review"] != nil {
		t.Fatalf("summary before submit: %v", rs["review"])
	}

	// Submitting writes the summary card with the style guide in mind.
	model.push(func(req llm.ChatRequest) []llm.ChatEvent {
		if req.ToolChoice != "report_review" || !strings.Contains(req.Messages[0].Content[0].Text, "+Every 4 years") || !strings.Contains(req.System[len(req.System)-1].Text, "numbers under ten") {
			t.Errorf("review request: %+v", req)
		}
		return call("rv", "report_review", `{"summary":"Shortens the laptop refresh to four years and adds exceptions.","commit_title":"Refresh laptops every four years","commit_body":"The refresh cycle moves from three to four years.","style_issues":[{"path":"docs/policy.md","quote":"Every 4 years","message":"Write numbers under ten as words."}]}`)
	})
	if code, body := samC.do("POST", "/revisions/"+revID+"/submit", map[string]any{"reviewers": []string{tom.ID}}); code != 200 {
		t.Fatalf("submit: %d %v", code, body)
	}
	runJobs(t, a)
	_, rs = tomC.do("GET", "/revisions/"+revID+"/review-summary", nil)
	review := rs["review"].(map[string]any)
	if review["summary"] != "Shortens the laptop refresh to four years and adds exceptions." || review["stale"] != false || review["commit_title"] != "Refresh laptops every four years" {
		t.Fatalf("review: %v", review)
	}
	if st := review["findings"].([]any); len(st) != 1 || st[0].(map[string]any)["kind"] != "style" {
		t.Fatalf("style findings: %v", st)
	}
	// The publish dialog starts from the suggested commit.
	if _, p := tomC.do("GET", "/revisions/"+revID+"/commit-preview", nil); p["title"] != "Refresh laptops every four years" || !strings.Contains(p["body"].(string), "three to four") {
		t.Fatalf("commit preview: %v", p)
	}

	// Tom edits during review: the summary is stale.
	rv, _ = revisions.Get(ctx, a.DB, revID)
	if err := a.Collab.Apply(ctx, repo, rv, revisions.Caller{User: tom, Role: access.Maintainer}, "docs/policy.md", strings.Replace(edited, "Every 4", "Every four", 1), "human"); err != nil {
		t.Fatal(err)
	}
	a.Collab.FlushRevision(ctx, revID)
	if _, rs := tomC.do("GET", "/revisions/"+revID+"/review-summary", nil); rs["review"].(map[string]any)["stale"] != true {
		t.Fatalf("not stale: %v", rs["review"])
	}

	// Publishing writes a one-line summary for readers.
	tomC.do("POST", "/revisions/"+revID+"/approve", nil)
	model.push(func(req llm.ChatRequest) []llm.ChatEvent {
		return say("Laptops are now replaced every four years, with no exceptions.")
	})
	if code, body := tomC.do("POST", "/revisions/"+revID+"/publish", map[string]any{"title": "Refresh laptops every four years"}); code != 202 {
		t.Fatalf("publish: %d %v", code, body)
	}
	runJobs(t, a)
	rv, _ = revisions.Get(ctx, a.DB, revID)
	_, cs := samC.do("GET", "/repos/"+repoID+"/change-summaries?path=docs/policy.md", nil)
	if got := cs["by_commit"].(map[string]any)[rv.PublishedSHA]; got != "Laptops are now replaced every four years, with no exceptions." {
		t.Fatalf("change summary: %v (sha %s)", cs, rv.PublishedSHA)
	}
}
