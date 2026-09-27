package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

func TestSaveRecoversColdDocumentAndResetsApprovals(t *testing.T) {
	for _, markdownPersisted := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_materialization", true: "before_approval_reset"}[markdownPersisted], func(t *testing.T) {
			a, admin := newApp(t, nil)
			ctx := context.Background()
			maya, _ := users.Create(ctx, a.DB, "maya@example.org", "Maya", true)
			signIn(t, a, admin, maya)
			repoID, remote := connectLocalRemote(t, a, admin, map[string]string{"docs/index.md": "# Initial\n"})
			_, body := admin.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Crash window", "path": "docs/index.md"})
			id := body["id"].(string)
			repo, _ := repos.Get(ctx, a.DB, repoID)
			rev, _ := revisions.Get(ctx, a.DB, id)
			if err := a.Collab.Apply(ctx, repo, rev, revisions.Caller{User: maya, Role: access.Admin}, "docs/index.md", "# Approved\n", "human"); err != nil {
				t.Fatal(err)
			}
			if code, b := admin.do("POST", "/revisions/"+id+"/submit", map[string]any{"reviewers": []string{maya.ID}}); code != 200 {
				t.Fatalf("submit %d %v", code, b)
			}
			if code, b := admin.do("POST", "/revisions/"+id+"/approve", nil); code != 200 {
				t.Fatalf("approve %d %v", code, b)
			}
			docID, state, err := a.Collab.StateOf(ctx, id, "docs/index.md")
			if err != nil {
				t.Fatal(err)
			}
			update, err := a.Engine.YApplyMarkdown(ctx, state, "# Accepted before crash\n", 777)
			if err != nil {
				t.Fatal(err)
			}
			// Model an update durably flushed just before the process crashed.
			if _, err := store.Exec(ctx, a.DB, `INSERT INTO ydoc_updates (ydoc_id, seq, data, user_id, created_at) SELECT ?, COALESCE(MAX(seq), 0) + 1, ?, ?, 9999999999999 FROM ydoc_updates WHERE ydoc_id = ?`, docID, update, maya.ID, docID); err != nil {
				t.Fatal(err)
			}
			if markdownPersisted {
				h := sha256.Sum256([]byte("# Accepted before crash\n"))
				if _, err := store.Exec(ctx, a.DB, `UPDATE revision_files SET content_md = ?, content_hash = ? WHERE revision_id = ? AND path = ?`, "# Accepted before crash\n", hex.EncodeToString(h[:16]), id, "docs/index.md"); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.Close(); err != nil {
				t.Fatal(err)
			}
			restarted, err := New(ctx, a.Config, a.Log)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = restarted.Close() })
			rev, _ = revisions.Get(ctx, restarted.DB, id)
			cp, err := restarted.Collab.Save(ctx, repo, rev, maya, "Recovered persisted edit")
			if err != nil {
				t.Fatalf("save cold persisted update: %v", err)
			}
			if got := gitIn(t, remote, "show", cp.CommitSHA+":docs/index.md"); got != "# Accepted before crash" {
				t.Fatalf("stale save: %q", got)
			}
			rev, _ = revisions.Get(ctx, restarted.DB, id)
			if rev.State != revisions.InReview {
				t.Fatalf("replayed edit retained stale approval: %s", rev.State)
			}
		})
	}
}
