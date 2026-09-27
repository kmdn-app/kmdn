package app

import (
	"context"
	"errors"
	"testing"

	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/updates"
	"github.com/kmdn-app/kmdn/internal/users"
)

func TestReviewDeletionConflictKeepsRequestedDeletion(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@example.org", "Maya", true)
	signIn(t, a, admin, maya)
	repoID, remote := connectLocalRemote(t, a, admin, map[string]string{"docs/index.md": "# Initial\n"})
	_, body := admin.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Delete page"})
	id := body["id"].(string)
	if code, b := admin.do("POST", "/revisions/"+id+"/files", map[string]any{"op": "delete", "path": "docs/index.md"}); code != 200 {
		t.Fatalf("delete %d %v", code, b)
	}
	writeFile(t, remote, "docs/index.md", "# Changed upstream\n")
	gitIn(t, remote, "commit", "--quiet", "-am", "Upstream edit")
	if err := a.Repos.Sync(ctx, repoID); err != nil {
		t.Fatal(err)
	}
	runJobs(t, a)
	u, err := updates.Pending(ctx, a.DB, id)
	if err != nil || u == nil {
		t.Fatalf("pending %v %v", u, err)
	}
	if code, b := admin.do("POST", "/revisions/"+id+"/updates/"+u.ID+"/apply", nil); code != 200 {
		t.Fatalf("apply %d %v", code, b)
	}
	if code, b := admin.do("POST", "/revisions/"+id+"/conflicts/resolve", map[string]any{"path": "docs/index.md", "choice": "delete"}); code != 204 {
		t.Fatalf("resolve %d %v", code, b)
	}
	f, err := revisions.FileAt(ctx, a.DB, id, "docs/index.md")
	if err != nil || f.Op != revisions.OpDelete {
		t.Fatalf("delete choice dropped local deletion: op=%s err=%v", f.Op, err)
	}
	rev, _ := revisions.Get(ctx, a.DB, id)
	if rev.HasConflicts || f.HasConflicts {
		t.Fatal("resolved deletion retains conflict flag")
	}
	if f.BaseMD != "# Changed upstream\n" {
		t.Fatalf("wrong updated base: %q", f.BaseMD)
	}
	admin.do("POST", "/revisions/"+id+"/submit", map[string]any{"reviewers": []string{maya.ID}})
	admin.do("POST", "/revisions/"+id+"/approve", nil)
	if code, b := admin.do("POST", "/revisions/"+id+"/publish", map[string]any{}); code != 202 {
		t.Fatalf("publish: %d %v", code, b)
	}
	runJobs(t, a)
	if _, err := gitInErr(remote, "show", "HEAD:docs/index.md"); err == nil {
		t.Fatal("resolved deletion did not remove published page")
	}
}

func TestReviewDeletionConflictKeepsPublishedPage(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@example.org", "Maya", true)
	signIn(t, a, admin, maya)
	repoID, remote := connectLocalRemote(t, a, admin, map[string]string{"docs/index.md": "# Initial\n"})
	_, body := admin.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Delete page"})
	id := body["id"].(string)
	if code, b := admin.do("POST", "/revisions/"+id+"/files", map[string]any{"op": "delete", "path": "docs/index.md"}); code != 200 {
		t.Fatalf("delete %d %v", code, b)
	}
	writeFile(t, remote, "docs/index.md", "# Changed upstream\n")
	gitIn(t, remote, "commit", "--quiet", "-am", "Upstream edit")
	if err := a.Repos.Sync(ctx, repoID); err != nil {
		t.Fatal(err)
	}
	runJobs(t, a)
	u, err := updates.Pending(ctx, a.DB, id)
	if err != nil || u == nil {
		t.Fatalf("pending %v %v", u, err)
	}
	if code, b := admin.do("POST", "/revisions/"+id+"/updates/"+u.ID+"/apply", nil); code != 200 {
		t.Fatalf("apply %d %v", code, b)
	}
	if code, b := admin.do("POST", "/revisions/"+id+"/conflicts/resolve", map[string]any{"path": "docs/index.md", "choice": "keep"}); code != 204 {
		t.Fatalf("resolve %d %v", code, b)
	}
	if _, err := revisions.FileAt(ctx, a.DB, id, "docs/index.md"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("keeping Published retained deletion: %v", err)
	}
	rev, _ := revisions.Get(ctx, a.DB, id)
	if rev.HasConflicts {
		t.Fatal("keeping Published did not clear conflict")
	}
	repo, _ := repos.Get(ctx, a.DB, repoID)
	page, err := a.Revisions.Read(ctx, repo, rev, "docs/index.md")
	if err != nil || page.Content != "# Changed upstream\n" {
		t.Fatalf("keeping Published lost content: %+v %v", page, err)
	}
}
