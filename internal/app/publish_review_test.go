package app

import (
	"context"
	"testing"
	"time"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/collab"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

func TestReviewPublishRespectsFlushReset(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	a.Collab.Options = collab.Options{FlushDelay: time.Hour, QuietPeriod: time.Hour}
	maya, _ := users.Create(ctx, a.DB, "maya@example.org", "Maya", true)
	signIn(t, a, admin, maya)
	repoID, remote := connectLocalRemote(t, a, admin, map[string]string{"docs/index.md": "# Initial\n"})
	tom, _ := users.Create(ctx, a.DB, "tom@example.org", "Tom", false)
	_ = access.Grant(ctx, a.DB, repoID, "user", tom.ID, access.Maintainer)
	tomC := &tc{t: t, base: admin.base, c: newClient()}
	signIn(t, a, tomC, tom)
	_, body := admin.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Review race", "path": "docs/index.md"})
	id := body["id"].(string)
	repo, _ := repos.Get(ctx, a.DB, repoID)
	rev, _ := revisions.Get(ctx, a.DB, id)
	if err := a.Collab.Apply(ctx, repo, rev, revisions.Caller{User: maya, Role: access.Admin}, "docs/index.md", "# Reviewed\n", "human"); err != nil {
		t.Fatal(err)
	}
	a.Collab.FlushRevision(ctx, id)
	if code, b := admin.do("POST", "/revisions/"+id+"/submit", map[string]any{"reviewers": []string{maya.ID, tom.ID}}); code != 200 {
		t.Fatalf("submit %d %v", code, b)
	}
	admin.do("POST", "/revisions/"+id+"/approve", nil)
	tomC.do("POST", "/revisions/"+id+"/approve", nil)
	rev, _ = revisions.Get(ctx, a.DB, id)
	if rev.State != revisions.Approved {
		t.Fatal(rev.State)
	}
	if err := a.Collab.Apply(ctx, repo, rev, revisions.Caller{User: tom, Role: access.Maintainer}, "docs/index.md", "# Not approved by Maya\n", "human"); err != nil {
		t.Fatal(err)
	}
	if code, b := tomC.do("POST", "/revisions/"+id+"/publish", map[string]any{}); code != 202 {
		t.Fatalf("enqueue %d %v", code, b)
	}
	runJobs(t, a)
	rev, _ = revisions.Get(ctx, a.DB, id)
	t.Logf("final state=%s; published content=%s", rev.State, gitIn(t, remote, "show", "HEAD:docs/index.md"))
	if rev.State != revisions.InReview {
		t.Fatalf("state %s after save reset another reviewer's approval, want in_review", rev.State)
	}
	if got := gitIn(t, remote, "show", "HEAD:docs/index.md"); got != "# Initial" {
		t.Fatalf("publish changed main after approval reset: %q", got)
	}
}

func TestPublishUsesSavedContentAfterLateMaterialization(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@example.org", "Maya", true)
	signIn(t, a, admin, maya)
	repoID, remote := connectLocalRemote(t, a, admin, map[string]string{"docs/index.md": "# Initial\n"})
	_, body := admin.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Pinned publish", "path": "docs/index.md"})
	id := body["id"].(string)
	repo, _ := repos.Get(ctx, a.DB, repoID)
	rev, _ := revisions.Get(ctx, a.DB, id)
	if err := a.Collab.Apply(ctx, repo, rev, revisions.Caller{User: maya, Role: access.Admin}, "docs/index.md", "# Approved snapshot\n", "human"); err != nil {
		t.Fatal(err)
	}
	a.Collab.FlushRevision(ctx, id)
	admin.do("POST", "/revisions/"+id+"/submit", map[string]any{"reviewers": []string{maya.ID}})
	admin.do("POST", "/revisions/"+id+"/approve", nil)
	checks := 0
	a.Revisions.PendingUpdates = func(ctx context.Context, rev revisions.Revision) (bool, error) {
		checks++
		if checks == 3 {
			// Model a room materializing after Save and the final state read.
			_, err := store.Exec(ctx, a.DB, `UPDATE revision_files SET content_md = ? WHERE revision_id = ? AND path = ?`, "# Unapproved late content\n", rev.ID, "docs/index.md")
			return false, err
		}
		return false, nil
	}
	if code, b := admin.do("POST", "/revisions/"+id+"/publish", map[string]any{}); code != 202 {
		t.Fatalf("publish: %d %v", code, b)
	}
	runJobs(t, a)
	if checks < 3 {
		t.Fatalf("late mutation not reached: %d", checks)
	}
	if got := gitIn(t, remote, "show", "HEAD:docs/index.md"); got != "# Approved snapshot" {
		t.Fatalf("published mutable content instead of saved branch: %q", got)
	}
}
