package app

import (
	"context"
	"errors"
	"testing"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

func TestReviewRestoreCheckpointAfterRename(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@example.org", "Maya", true)
	signIn(t, a, admin, maya)
	repoID, _ := connectLocalRemote(t, a, admin, map[string]string{"docs/index.md": "# Initial\n"})
	_, body := admin.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Restore page", "path": "docs/index.md"})
	id := body["id"].(string)
	repo, _ := repos.Get(ctx, a.DB, repoID)
	rev, _ := revisions.Get(ctx, a.DB, id)
	c := revisions.Caller{User: maya, Role: access.Admin}
	if err := a.Collab.Apply(ctx, repo, rev, c, "docs/index.md", "# Saved content\n", "human"); err != nil {
		t.Fatal(err)
	}
	cp, err := a.Collab.Save(ctx, repo, rev, maya, "")
	if err != nil {
		t.Fatal(err)
	}
	if code, b := admin.do("POST", "/revisions/"+id+"/files", map[string]any{"op": "rename", "from_path": "docs/index.md", "path": "docs/moved.md"}); code != 200 {
		t.Fatalf("rename %d %v", code, b)
	}
	rev, _ = revisions.Get(ctx, a.DB, id)
	if err := a.Collab.Restore(ctx, repo, rev, c, cp.ID); err != nil {
		t.Fatalf("restore checkpoint after rename: %v", err)
	}
	f, err := revisions.FileAt(ctx, a.DB, id, "docs/index.md")
	if err != nil || f.ContentMD != "# Saved content\n" {
		t.Fatalf("restored file: %+v %v", f, err)
	}
	if _, err := revisions.FileAt(ctx, a.DB, id, "docs/moved.md"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("later path remains: %v", err)
	}
}

func TestReviewRestoreCheckpointAfterReusingOriginalPath(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@example.org", "Maya", true)
	signIn(t, a, admin, maya)
	repoID, _ := connectLocalRemote(t, a, admin, map[string]string{"docs/index.md": "# Initial\n"})
	_, body := admin.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Restore page", "path": "docs/index.md"})
	id := body["id"].(string)
	repo, _ := repos.Get(ctx, a.DB, repoID)
	rev, _ := revisions.Get(ctx, a.DB, id)
	c := revisions.Caller{User: maya, Role: access.Admin}
	if err := a.Collab.Apply(ctx, repo, rev, c, "docs/index.md", "# Saved content\n", "human"); err != nil {
		t.Fatal(err)
	}
	cp, err := a.Collab.Save(ctx, repo, rev, maya, "")
	if err != nil {
		t.Fatal(err)
	}
	if code, b := admin.do("POST", "/revisions/"+id+"/files", map[string]any{"op": "rename", "from_path": "docs/index.md", "path": "docs/moved.md"}); code != 200 {
		t.Fatalf("rename %d %v", code, b)
	}
	if code, b := admin.do("POST", "/revisions/"+id+"/files", map[string]any{"op": "add", "path": "docs/index.md", "content": "# Replacement\n"}); code != 200 {
		t.Fatalf("re-add %d %v", code, b)
	}
	rev, _ = revisions.Get(ctx, a.DB, id)
	if err := a.Collab.Restore(ctx, repo, rev, c, cp.ID); err != nil {
		t.Fatalf("restore checkpoint after rename: %v", err)
	}
	f, err := revisions.FileAt(ctx, a.DB, id, "docs/index.md")
	if err != nil || f.ContentMD != "# Saved content\n" {
		t.Fatalf("restored file: %+v %v", f, err)
	}
	if _, err := revisions.FileAt(ctx, a.DB, id, "docs/moved.md"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("later path remains: %v", err)
	}
}
