package collab

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kmdn-app/kmdn/internal/branches"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/storetest"
)

// storetest runs this against Postgres too when KMDN_TEST_POSTGRES_URL is set.
func TestSaveIntentFinalizationIsAtomic(t *testing.T) {
	db := storetest.Open(t)
	ctx := context.Background()
	for _, query := range []string{
		`INSERT INTO users (id, email, name, created_at) VALUES ('usr_save', 'save@example.org', 'Saver', 1)`,
		`INSERT INTO forge_hosts (id, kind, display_name, created_at) VALUES ('host_save', 'git', 'Git', 1)`,
		`INSERT INTO repos (org_id, id, forge_host_id, owner, name, display_name, target_branch, created_at) VALUES ('org_default', 'repo_save', 'host_save', 'owner', 'repo', 'Repo', 'main', 1)`,
		`INSERT INTO revisions (id, repo_id, number, title, base_sha, created_at, updated_at, branch, branch_sha, branch_base_sha) VALUES ('rev_save', 'repo_save', 1, 'Save', 'base', 1, 1, 'kmdn/save', 'prior', 'base')`,
	} {
		if _, err := store.Exec(ctx, db, query); err != nil {
			t.Fatal(err)
		}
	}
	h := &Hub{DB: db}
	in := saveIntent{
		Checkpoint: CheckpointView{ID: "cp_save", CreatedBy: "usr_save", CreatedAt: time.Now(), CommitSHA: "intended"},
		Branch:     "kmdn/save", PriorSHA: "prior", BaseSHA: "base", Hash: "captured", Objects: []byte("captured-pack"),
		Payload: savePayload{Files: []savedFile{{Path: "docs/index.md", Op: revisions.OpModify, Content: "# Captured\n", State: []byte{1, 2, 3}}}},
	}
	if err := h.putSaveIntent(ctx, "rev_save", in); err != nil {
		t.Fatal(err)
	}
	// Fail the metadata comparison after inserting the checkpoint and files.
	if _, err := store.Exec(ctx, db, `UPDATE revisions SET branch_sha = 'changed' WHERE id = 'rev_save'`); err != nil {
		t.Fatal(err)
	}
	if err := h.finishSave(ctx, revisions.Revision{ID: "rev_save"}, in); !errors.Is(err, branches.ErrBranchMoved) {
		t.Fatalf("expected metadata conflict: %v", err)
	}
	for _, table := range []string{"revision_checkpoints", "revision_checkpoint_files", "revision_events"} {
		var n int
		if err := store.QueryRow(ctx, db, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("partial finalization in %s: %d, %v", table, n, err)
		}
	}
	var n int
	if err := store.QueryRow(ctx, db, `SELECT COUNT(*) FROM revision_save_intents`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("failed finalization lost intent: %d, %v", n, err)
	}
	if _, err := store.Exec(ctx, db, `UPDATE revisions SET branch_sha = 'prior' WHERE id = 'rev_save'`); err != nil {
		t.Fatal(err)
	}
	if err := h.finishSave(ctx, revisions.Revision{ID: "rev_save"}, in); err != nil {
		t.Fatal(err)
	}
	var sha, base, content, hash string
	var state []byte
	if err := store.QueryRow(ctx, db, `SELECT branch_sha, branch_base_sha FROM revisions WHERE id = 'rev_save'`).Scan(&sha, &base); err != nil || sha != "intended" || base != "base" {
		t.Fatalf("final metadata: %s %s, %v", sha, base, err)
	}
	if err := store.QueryRow(ctx, db, `SELECT f.content_md, f.ydoc_state, c.content_hash FROM revision_checkpoint_files f JOIN revision_checkpoints c ON c.id = f.checkpoint_id WHERE c.id = 'cp_save'`).Scan(&content, &state, &hash); err != nil || content != "# Captured\n" || hash != "captured" || !bytes.Equal(state, []byte{1, 2, 3}) {
		t.Fatalf("immutable checkpoint: %q %v %s, %v", content, state, hash, err)
	}
	if err := store.QueryRow(ctx, db, `SELECT COUNT(*) FROM revision_save_intents`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("completed intent remains: %d, %v", n, err)
	}
}
