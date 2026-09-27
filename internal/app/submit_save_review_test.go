package app

import (
	"context"
	"testing"

	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

func TestReviewSubmitStopsWhenSaveFails(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@example.org", "Maya", true)
	signIn(t, a, admin, maya)
	repoID, _ := connectLocalRemote(t, a, admin, map[string]string{"docs/index.md": "# Initial\n"})
	_, body := admin.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Fail submit save", "path": "docs/index.md"})
	id := body["id"].(string)
	runJobs(t, a)
	if _, err := store.Exec(ctx, a.DB, `CREATE TRIGGER fail_save BEFORE UPDATE OF branch_sha ON revisions BEGIN SELECT RAISE(FAIL, 'simulated disk failure'); END`); err != nil {
		t.Fatal(err)
	}
	code, b := admin.do("POST", "/revisions/"+id+"/submit", map[string]any{"reviewers": []string{maya.ID}})
	rev, _ := revisions.Get(ctx, a.DB, id)
	t.Logf("submit response=%d state=%s body=%v", code, rev.State, b)
	if rev.State != revisions.Editing {
		t.Fatal("submission proceeded despite failed save")
	}
	if code < 400 {
		t.Fatalf("save failure returned success: %d", code)
	}
	var submitted int
	if err := store.QueryRow(ctx, a.DB, `SELECT COUNT(*) FROM revision_events WHERE revision_id = ? AND kind = 'submitted'`, id).Scan(&submitted); err != nil {
		t.Fatal(err)
	}
	if submitted != 0 {
		t.Fatal("failed submit emitted review event")
	}
}
