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

func runJobs(t *testing.T, a *App) {
	t.Helper()
	for {
		ran, err := a.Jobs.RunOnce(context.Background())
		if err != nil {
			t.Logf("job error: %v", err)
		}
		if !ran {
			return
		}
	}
}

// TestPublishMergesTheRevisionBranch: publishing merges the revision's
// branch with a merge commit carrying the attribution; the commits people
// saved stay in history.
func TestPublishMergesTheRevisionBranch(t *testing.T) {
	a, admin := newApp(t, nil)
	a.Collab.Options = collab.Options{FlushDelay: time.Millisecond, QuietPeriod: time.Millisecond}
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	repoID, remote := connectLocalRemote(t, a, admin, map[string]string{
		"docs/index.md":  "# Handbook\n\nStart   here.\n",
		"docs/old.md":    "# Old\n",
		"docs/guide.md":  "# Guide\n",
		"docs/intact.md": "# Untouched\n",
	})
	mk := func(email, name string, role access.Role) (users.User, *tc) {
		u, _ := users.Create(ctx, a.DB, email, name, false)
		_ = access.Grant(ctx, a.DB, repoID, "user", u.ID, role)
		c := &tc{t: t, base: admin.base, c: newClient()}
		signIn(t, a, c, u)
		return u, c
	}
	sam, samC := mk("sam@northwind.dev", "Sam Lindqvist", access.Contributor)
	tom, tomC := mk("tom@northwind.dev", "Tom Okafor", access.Maintainer)
	// Sam's co-author address is a custom one.
	samC.do("PUT", "/me/commit-email", map[string]any{"mode": "custom", "custom": "sam.l@example.org"})

	_, rev := samC.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Refresh the handbook", "description": "Clearer start and a cleanup.", "path": "docs/index.md"})
	revID := rev["id"].(string)
	repo, _ := repos.Get(ctx, a.DB, repoID)
	apply := func(u users.User, role access.Role, p, md string) {
		t.Helper()
		r, _ := revisions.Get(ctx, a.DB, revID)
		if err := a.Collab.Apply(ctx, repo, r, revisions.Caller{User: u, Role: role}, p, md, "human"); err != nil {
			t.Fatal(err)
		}
		a.Collab.Flush(ctx)
	}
	apply(sam, access.Contributor, "docs/index.md", "# Handbook\n\nStart   here. It's the place to begin, with everything you need.\n")
	samC.do("POST", "/revisions/"+revID+"/files", map[string]any{"op": "delete", "path": "docs/old.md"})
	samC.do("POST", "/revisions/"+revID+"/files", map[string]any{"op": "rename", "from_path": "docs/guide.md", "path": "docs/guides/guide.md"})
	samC.do("POST", "/revisions/"+revID+"/files", map[string]any{"op": "add", "path": "docs/new.md", "content": "# New page\n"})
	_, r := samC.do("POST", "/revisions/"+revID+"/submit", map[string]any{"reviewers": []string{tom.ID}})
	if r["state"] != "in_review" {
		t.Fatalf("submit: %v", r)
	}
	// The reviewer fixes a typo (and is credited too), then approves.
	apply(tom, access.Maintainer, "docs/index.md", "# Handbook\n\nStart   here. It's the place to begin, with everything you need!\n")
	tomC.do("POST", "/revisions/"+revID+"/approve", nil)

	// Contributors can't publish; maintainers can.
	if code, _ := samC.do("POST", "/revisions/"+revID+"/publish", map[string]any{}); code != 403 {
		t.Fatalf("contributor publish: %d", code)
	}
	code, pv := tomC.do("GET", "/revisions/"+revID+"/commit-preview", nil)
	if code != 200 || pv["blocked"] != nil {
		t.Fatalf("preview: %d %v", code, pv)
	}
	msg := pv["message"].(string)
	for _, want := range []string{
		"Refresh the handbook\n\nClearer start and a cleanup.\n\nKmdn-Revision: ",
		"/revisions/1\nCo-authored-by: Sam Lindqvist <sam.l@example.org>\nCo-authored-by: Tom Okafor <tom@northwind.dev>\nReviewed-by: Tom Okafor <tom@northwind.dev>\n",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message missing %q:\n%s", want, msg)
		}
	}
	if code, body := tomC.do("POST", "/revisions/"+revID+"/publish", map[string]any{"title": "Refresh the handbook"}); code != 202 {
		t.Fatalf("publish: %d %v", code, body)
	}
	runJobs(t, a)

	_, r = samC.do("GET", "/revisions/"+revID, nil)
	sha, _ := r["published_sha"].(string)
	if r["state"] != "published" || sha == "" {
		t.Fatalf("after publish: %v", r)
	}
	if got := gitIn(t, remote, "rev-parse", "HEAD"); got != sha {
		t.Fatalf("remote head %s, published %s", got, sha)
	}
	if got := gitIn(t, remote, "log", "-1", "--format=%an|%B"); !strings.HasPrefix(got, "kmdn|Refresh the handbook") || !strings.Contains(got, "Co-authored-by: Sam Lindqvist <sam.l@example.org>") {
		t.Fatalf("commit: %q", got)
	}
	// A merge of main and the branch. The branch holds the start commit,
	// Sam's save on submit and the save of Tom's later fix before publishing.
	if parents := strings.Fields(gitIn(t, remote, "log", "-1", "--format=%P")); len(parents) != 2 {
		t.Fatalf("not a merge: %v", parents)
	}
	if got := gitIn(t, remote, "log", "--format=%an|%s", "HEAD^1..HEAD^2"); got != "Tom Okafor|Save before publishing\nSam Lindqvist|Save for review\nSam Lindqvist|Start revision #1: Refresh the handbook" {
		t.Fatalf("saved commits: %q", got)
	}
	if got := gitIn(t, remote, "branch", "--list", "kmdn/*"); got != "" {
		t.Fatalf("merged branch not deleted: %q", got)
	}
	if got := gitIn(t, remote, "ls-tree", "-r", "--name-only", "HEAD"); got != "docs/guides/guide.md\ndocs/index.md\ndocs/intact.md\ndocs/new.md" {
		t.Fatalf("tree: %q", got)
	}
	if got := gitIn(t, remote, "show", "HEAD:docs/index.md"); got != "# Handbook\n\nStart   here. It's the place to begin, with everything you need!" {
		t.Fatalf("index.md: %q", got)
	}
	// The repo syncs to the published commit; the revision is read-only.
	if _, rv := admin.do("GET", "/repos/"+repoID, nil); rv["head_sha"] != sha {
		t.Fatalf("repo head: %v", rv["head_sha"])
	}
	if code, _ := samC.do("POST", "/revisions/"+revID+"/files", map[string]any{"op": "add", "path": "docs/late.md"}); code != 403 {
		t.Fatalf("edit after publish: %d", code)
	}

	// Published moving under a revision's pages blocks publishing it.
	_, rev2 := samC.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Second", "path": "docs/intact.md"})
	rev2ID := rev2["id"].(string)
	r2, _ := revisions.Get(ctx, a.DB, rev2ID)
	if err := a.Collab.Apply(ctx, repo, r2, revisions.Caller{User: sam, Role: access.Contributor}, "docs/intact.md", "# Untouched, now touched\n", "human"); err != nil {
		t.Fatal(err)
	}
	a.Collab.Flush(ctx)
	samC.do("POST", "/revisions/"+rev2ID+"/submit", map[string]any{"reviewers": []string{tom.ID}})
	tomC.do("POST", "/revisions/"+rev2ID+"/approve", nil)
	writeFile(t, remote, "docs/intact.md", "# Changed on the forge\n")
	gitIn(t, remote, "commit", "--quiet", "-am", "Edit on the forge")
	tomC.do("POST", "/revisions/"+rev2ID+"/publish", map[string]any{})
	runJobs(t, a)
	if _, r := samC.do("GET", "/revisions/"+rev2ID, nil); r["state"] != "approved" {
		t.Fatalf("publish over a moved page should stop: %v", r["state"])
	}
	_, ev := samC.do("GET", "/revisions/"+rev2ID+"/events", nil)
	if !strings.Contains(toJSON(ev), `"publish_blocked"`) {
		t.Fatalf("no publish_blocked event: %v", ev)
	}
}

// A conflict from Published resolved by keeping the revision's text leaves
// the pages as they were saved, but the branch still has to merge the new
// base: without that commit the pull request conflicts and the forge
// refuses to merge it.
func TestPublishAfterKeepingTheRevisionInAConflict(t *testing.T) {
	a, admin := newApp(t, nil)
	a.Collab.Options = collab.Options{FlushDelay: time.Millisecond, QuietPeriod: time.Millisecond}
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	const page = "docs/onboarding/it-setup.md"
	repoID, remote := connectLocalRemote(t, a, admin, map[string]string{page: "# IT setup\n\nAsk IT for a laptop.\n"})
	tom, _ := users.Create(ctx, a.DB, "tom@northwind.dev", "Tom Okafor", false)
	_ = access.Grant(ctx, a.DB, repoID, "user", tom.ID, access.Maintainer)
	tomC := &tc{t: t, base: admin.base, c: newClient()}
	signIn(t, a, tomC, tom)

	_, body := admin.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Laptops", "path": page})
	revID := body["id"].(string)
	runJobs(t, a)
	repo, _ := repos.Get(ctx, a.DB, repoID)
	apply := func(md string) {
		t.Helper()
		rev, _ := revisions.Get(ctx, a.DB, revID)
		if err := a.Collab.Apply(ctx, repo, rev, revisions.Caller{User: maya, Role: access.Admin}, page, md, "human"); err != nil {
			t.Fatal(err)
		}
		a.Collab.FlushRevision(ctx, revID)
	}
	unsaved := func() bool {
		t.Helper()
		_, r := admin.do("GET", "/revisions/"+revID, nil)
		return r["unsaved_changes"] == true
	}
	const ours = "# IT setup\n\nOrder a laptop from the portal.\n"
	apply(ours)
	if _, r := admin.do("POST", "/revisions/"+revID+"/submit", map[string]any{"reviewers": []string{tom.ID}}); r["state"] != "in_review" {
		t.Fatalf("submit: %v", r)
	}
	savedHash, _ := revisions.SavedHash(ctx, a.DB, revID)

	// Someone changes the same line on main; the reviewer applies it.
	writeFile(t, remote, page, "# IT setup\n\nAsk IT for a laptop on day one.\n")
	gitIn(t, remote, "commit", "--quiet", "-am", "Laptops on main")
	head := gitIn(t, remote, "rev-parse", "main")
	admin.do("POST", "/repos/"+repoID+"/refresh", nil)
	runJobs(t, a)
	_, body = tomC.do("GET", "/revisions/"+revID+"/updates", nil)
	up, _ := body["update"].(map[string]any)
	if up == nil || up["conflicts"] != float64(1) {
		t.Fatalf("pending update: %v", body)
	}
	if code, res := tomC.do("POST", "/revisions/"+revID+"/updates/"+up["id"].(string)+"/apply", nil); code != 200 || res["reopened"] != true {
		t.Fatalf("apply: %d %v", code, res)
	}

	// The editor keeps the revision's text: the content is what was saved...
	apply(ours)
	if rev, _ := revisions.Get(ctx, a.DB, revID); rev.HasConflicts || rev.BaseSHA != head {
		t.Fatalf("after resolving: conflicts %v, base %s", rev.HasConflicts, rev.BaseSHA)
	}
	if now, _ := revisions.ContentHash(ctx, a.DB, revID); now != savedHash {
		t.Fatalf("content differs from the save: %s vs %s", now, savedHash)
	}
	// ...but the new base isn't on the branch yet, so there is something to save.
	if !unsaved() {
		t.Fatal("applied updates not reported as unsaved")
	}

	// Resubmitting saves: a merge of the new base.
	if _, r := admin.do("POST", "/revisions/"+revID+"/submit", map[string]any{"reviewers": []string{tom.ID}}); r["state"] != "in_review" {
		t.Fatalf("resubmit: %v", r)
	}
	rev, _ := revisions.Get(ctx, a.DB, revID)
	if parents := strings.Fields(gitIn(t, remote, "log", "-1", "--format=%P", rev.Branch)); len(parents) != 2 || parents[1] != head {
		t.Fatalf("branch tip parents %v, want the previous save and %s", parents, head)
	}
	if out, err := gitInErr(remote, "merge-tree", "--write-tree", "main", rev.Branch); err != nil {
		t.Fatalf("the pull request conflicts: %s", out)
	}
	if unsaved() {
		t.Fatal("still unsaved after the merge commit")
	}
	if code, b := tomC.do("POST", "/revisions/"+revID+"/save", map[string]any{}); code != 409 || b["code"] != "nothing_to_save" {
		t.Fatalf("second save: %d %v", code, b)
	}

	tomC.do("POST", "/revisions/"+revID+"/approve", nil)
	if code, b := tomC.do("POST", "/revisions/"+revID+"/publish", map[string]any{}); code != 202 {
		t.Fatalf("publish: %d %v", code, b)
	}
	runJobs(t, a)
	_, r := admin.do("GET", "/revisions/"+revID, nil)
	if r["state"] != "published" {
		t.Fatalf("after publish: %v", r)
	}
	if got := gitIn(t, remote, "rev-parse", "HEAD^2"); got != rev.BranchSHA {
		t.Fatalf("published merge's second parent %s, branch %s", got, rev.BranchSHA)
	}
	if got := gitIn(t, remote, "show", "HEAD:"+page); !strings.Contains(got, "Order a laptop from the portal.") {
		t.Fatalf("published page: %q", got)
	}
}
