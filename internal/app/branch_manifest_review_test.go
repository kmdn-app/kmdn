package app

import (
	"context"
	"errors"
	"testing"

	"github.com/kmdn-app/kmdn/internal/gitmirror"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/users"
)

func TestSavePreservesFilesReusingRenameSources(t *testing.T) {
	for _, tc := range []struct {
		name    string
		initial map[string]string
		ops     []revisions.FileOp
		want    map[string]string
		publish bool
	}{
		{
			name:    "rename sorts after replacement",
			initial: map[string]string{"docs/a.md": "# Original\n"},
			ops: []revisions.FileOp{
				{Op: revisions.OpRename, FromPath: "docs/a.md", Path: "docs/z.md"},
				{Op: revisions.OpAdd, Path: "docs/a.md", Content: "# Replacement\n"},
			},
			want:    map[string]string{"docs/a.md": "# Replacement\n", "docs/z.md": "# Original\n"},
			publish: true,
		},
		{
			name:    "rename sorts before replacement",
			initial: map[string]string{"docs/z.md": "# Original\n"},
			ops: []revisions.FileOp{
				{Op: revisions.OpRename, FromPath: "docs/z.md", Path: "docs/a.md"},
				{Op: revisions.OpAdd, Path: "docs/z.md", Content: "# Replacement\n"},
			},
			want: map[string]string{"docs/z.md": "# Replacement\n", "docs/a.md": "# Original\n"},
		},
		{
			name:    "rename chain",
			initial: map[string]string{"docs/a.md": "# First\n", "docs/b.md": "# Second\n"},
			ops: []revisions.FileOp{
				{Op: revisions.OpRename, FromPath: "docs/b.md", Path: "docs/c.md"},
				{Op: revisions.OpRename, FromPath: "docs/a.md", Path: "docs/b.md"},
			},
			want: map[string]string{"docs/b.md": "# First\n", "docs/c.md": "# Second\n"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, admin := newApp(t, nil)
			ctx := context.Background()
			maya, err := users.Create(ctx, a.DB, "maya@example.org", "Maya", true)
			if err != nil {
				t.Fatal(err)
			}
			signIn(t, a, admin, maya)
			tc.initial["docs/deleted.md"] = "# Remove\n"
			tc.initial["docs/unchanged.md"] = "# Keep\n"
			repoID, remote := connectLocalRemote(t, a, admin, tc.initial)
			code, body := admin.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Rename pages", "path": tc.ops[0].FromPath})
			if code != 201 {
				t.Fatalf("create revision %d: %v", code, body)
			}
			id := body["id"].(string)
			for _, op := range append(tc.ops, revisions.FileOp{Op: revisions.OpDelete, Path: "docs/deleted.md"}) {
				if code, body := admin.do("POST", "/revisions/"+id+"/files", op); code != 200 {
					t.Fatalf("file operation %+v: %d %v", op, code, body)
				}
			}
			repo, err := repos.Get(ctx, a.DB, repoID)
			if err != nil {
				t.Fatal(err)
			}
			rev, err := revisions.Get(ctx, a.DB, id)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.Collab.Save(ctx, repo, rev, maya, ""); err != nil {
				t.Fatal(err)
			}
			rev, err = revisions.Get(ctx, a.DB, id)
			if err != nil {
				t.Fatal(err)
			}
			tc.want["docs/unchanged.md"] = "# Keep\n"
			m := a.Repos.Mirror(repo)
			for path, want := range tc.want {
				got, err := m.ReadFile(ctx, nil, rev.BranchSHA, path)
				if err != nil || string(got) != want {
					t.Errorf("saved %s = %q, %v; want %q", path, got, err, want)
				}
			}
			for path := range tc.initial {
				if _, keep := tc.want[path]; keep {
					continue
				}
				if _, err := m.ReadFile(ctx, nil, rev.BranchSHA, path); !errors.Is(err, gitmirror.ErrNotFound) {
					t.Errorf("removed path %s remains: %v", path, err)
				}
			}
			if unsaved, err := revisions.Unsaved(ctx, a.DB, id); err != nil || unsaved {
				t.Fatalf("saved revision is dirty: %v, %v", unsaved, err)
			}
			if tc.publish {
				if code, body := admin.do("POST", "/revisions/"+id+"/submit", map[string]any{"reviewers": []string{maya.ID}}); code != 200 {
					t.Fatalf("submit: %d %v", code, body)
				}
				if code, body := admin.do("POST", "/revisions/"+id+"/approve", nil); code != 200 {
					t.Fatalf("approve: %d %v", code, body)
				}
				if code, body := admin.do("POST", "/revisions/"+id+"/publish", map[string]any{}); code != 202 {
					t.Fatalf("publish: %d %v", code, body)
				}
				runJobs(t, a)
				for path, want := range tc.want {
					got, err := gitInErr(remote, "show", "HEAD:"+path)
					if err != nil || got != want {
						t.Errorf("published %s = %q, %v; want %q", path, got, err, want)
					}
				}
				if _, err := gitInErr(remote, "show", "HEAD:docs/deleted.md"); err == nil {
					t.Fatal("deleted page remains after publish")
				}
			}
		})
	}
}
