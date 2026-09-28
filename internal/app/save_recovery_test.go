package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/branches"
	"github.com/kmdn-app/kmdn/internal/collab"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

func TestSaveRecoversPushedCommitAfterRestart(t *testing.T) {
	sqliteOnly(t)
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@example.org", "Maya", true)
	signIn(t, a, admin, maya)
	repoID, remote := connectLocalRemote(t, a, admin, map[string]string{"docs/index.md": "# Initial\n"})
	_, body := admin.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Recover save", "path": "docs/index.md"})
	id := body["id"].(string)
	runJobs(t, a)
	repo, _ := repos.Get(ctx, a.DB, repoID)
	rev, _ := revisions.Get(ctx, a.DB, id)
	c := revisions.Caller{User: maya, Role: access.Admin}
	if err := a.Collab.Apply(ctx, repo, rev, c, "docs/index.md", "# Intended save\n", "human"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Exec(ctx, a.DB, `CREATE TRIGGER fail_save BEFORE UPDATE OF branch_sha ON revisions BEGIN SELECT RAISE(FAIL, 'simulated disk failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Collab.Save(ctx, repo, rev, maya, "Original save message"); err == nil {
		t.Fatal("save should fail after pushing")
	}
	pushed := gitIn(t, remote, "rev-parse", rev.Branch)
	if pushed == rev.BranchSHA {
		t.Fatal("failure did not follow a push")
	}
	assertSaveJournalCounts(t, a.DB, id, 1, 0)
	if got := gitIn(t, a.Repos.Mirror(repo).Path, "rev-parse", branches.SaveRef(id)); got != pushed {
		t.Fatalf("prepared commit is not retained: %s", got)
	}
	if _, err := store.Exec(ctx, a.DB, `DROP TRIGGER fail_save`); err != nil {
		t.Fatal(err)
	}
	if err := a.Collab.Apply(ctx, repo, rev, c, "docs/index.md", "# Newer edit\n", "human"); err != nil {
		t.Fatal(err)
	}
	a.Collab.FlushRevision(ctx, id)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	// A backup can restore the database without its disposable mirror cache.
	if err := os.RemoveAll(a.Repos.Mirror(repo).Path); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(ctx, a.Config, a.Log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	rev, _ = revisions.Get(ctx, restarted.DB, id)
	cp, err := restarted.Collab.Save(ctx, repo, rev, maya, "A retry must not replace the original save")
	if err != nil {
		t.Fatalf("recover pushed save: %v", err)
	}
	if cp.CommitSHA != pushed || cp.Name != "Original save message" || cp.CreatedBy != maya.ID {
		t.Fatalf("recovery changed the intended save: %+v", cp)
	}
	var content string
	if err := store.QueryRow(ctx, restarted.DB, `SELECT content_md FROM revision_checkpoint_files WHERE checkpoint_id = ? AND path = ?`, cp.ID, "docs/index.md").Scan(&content); err != nil || content != "# Intended save\n" {
		t.Fatalf("checkpoint content %q: %v", content, err)
	}
	var state []byte
	var sourceMap string
	if err := store.QueryRow(ctx, restarted.DB, `SELECT ydoc_state FROM revision_checkpoint_files WHERE checkpoint_id = ? AND path = ?`, cp.ID, "docs/index.md").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := store.QueryRow(ctx, restarted.DB, `SELECT source_map FROM ydocs WHERE revision_id = ? AND path = ?`, id, "docs/index.md").Scan(&sourceMap); err != nil {
		t.Fatal(err)
	}
	if md, err := restarted.Engine.YMaterialize(ctx, state, sourceMap); err != nil || md != content {
		t.Fatalf("checkpoint document differs from committed content: %q, %v", md, err)
	}
	assertSaveJournalCounts(t, restarted.DB, id, 0, 1)
	if got := gitIn(t, remote, "log", "-1", "--format=%an|%ae|%s", pushed); got != "Maya|maya@example.org|Original save message" {
		t.Fatalf("author or message changed: %q", got)
	}
	if dirty, err := revisions.Unsaved(ctx, restarted.DB, id); err != nil || !dirty {
		t.Fatalf("newer edits must remain unsaved: %v, %v", dirty, err)
	}
	if got := gitIn(t, remote, "rev-parse", rev.Branch); got != pushed {
		t.Fatalf("recovery created a replacement commit: %s", got)
	}
	rev, _ = revisions.Get(ctx, restarted.DB, id)
	next, err := restarted.Collab.Save(ctx, repo, rev, maya, "Save newer edit")
	if err != nil {
		t.Fatal(err)
	}
	if got := gitIn(t, remote, "rev-parse", next.CommitSHA+"^"); got != pushed {
		t.Fatalf("next save lost history: %s", got)
	}
	rev, _ = revisions.Get(ctx, restarted.DB, id)
	if _, err := restarted.Collab.Save(ctx, repo, rev, maya, ""); !errors.Is(err, collab.ErrNothingToSave) {
		t.Fatalf("no-op retry: %v", err)
	}
	assertSaveJournalCounts(t, restarted.DB, id, 0, 2)
}

func assertSaveJournalCounts(t *testing.T, db *store.DB, revID string, intents, checkpoints int) {
	t.Helper()
	for _, want := range []struct {
		query string
		count int
	}{
		{`SELECT COUNT(*) FROM revision_save_intents WHERE revision_id = ?`, intents},
		{`SELECT COUNT(*) FROM revision_checkpoints WHERE revision_id = ?`, checkpoints},
		{`SELECT COUNT(*) FROM revision_events WHERE revision_id = ? AND kind = 'saved'`, checkpoints},
	} {
		var got int
		if err := store.QueryRow(context.Background(), db, want.query, revID).Scan(&got); err != nil || got != want.count {
			t.Fatalf("%s: got %d, want %d, err %v", want.query, got, want.count, err)
		}
	}
}

func TestSaveRecoversBeforePushAndAtomicFinalization(t *testing.T) {
	sqliteOnly(t)
	for _, failure := range []string{"before_push", "checkpoint_insert", "intent_delete", "before_intent"} {
		t.Run(failure, func(t *testing.T) {
			a, admin := newApp(t, nil)
			ctx := context.Background()
			maya, _ := users.Create(ctx, a.DB, "maya@example.org", "Maya", true)
			signIn(t, a, admin, maya)
			repoID, remote := connectLocalRemote(t, a, admin, map[string]string{"docs/index.md": "# Initial\n"})
			_, body := admin.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Interrupted save", "path": "docs/index.md"})
			id := body["id"].(string)
			runJobs(t, a)
			repo, _ := repos.Get(ctx, a.DB, repoID)
			rev, _ := revisions.Get(ctx, a.DB, id)
			if err := a.Collab.Apply(ctx, repo, rev, revisions.Caller{User: maya, Role: access.Admin}, "docs/index.md", "# Captured\n", "human"); err != nil {
				t.Fatal(err)
			}
			hook := filepath.Join(remote, ".git", "hooks", "pre-receive")
			if failure == "before_push" {
				if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			} else {
				event := map[string]string{"checkpoint_insert": "INSERT ON revision_checkpoints", "intent_delete": "DELETE ON revision_save_intents", "before_intent": "INSERT ON revision_save_intents"}[failure]
				if _, err := store.Exec(ctx, a.DB, "CREATE TRIGGER interrupt_save BEFORE "+event+" BEGIN SELECT RAISE(FAIL, 'interrupted'); END"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := a.Collab.Save(ctx, repo, rev, maya, "Captured save"); err == nil {
				t.Fatal("save should fail at injected boundary")
			}
			intents := 1
			if failure == "before_intent" {
				intents = 0
			}
			assertSaveJournalCounts(t, a.DB, id, intents, 0)
			var intended string
			if intents == 1 {
				if err := store.QueryRow(ctx, a.DB, `SELECT commit_sha FROM revision_save_intents WHERE revision_id = ?`, id).Scan(&intended); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "before_push" || failure == "before_intent" {
				if got := gitIn(t, remote, "rev-parse", rev.Branch); got != rev.BranchSHA {
					t.Fatalf("push preceded durable preparation: %s", got)
				}
			}
			stored, _ := revisions.Get(ctx, a.DB, id)
			if stored.BranchSHA != rev.BranchSHA {
				t.Fatal("failed transaction changed branch metadata")
			}
			if failure == "before_push" {
				if err := os.Remove(hook); err != nil {
					t.Fatal(err)
				}
			} else if _, err := store.Exec(ctx, a.DB, `DROP TRIGGER interrupt_save`); err != nil {
				t.Fatal(err)
			}
			if err := a.Close(); err != nil {
				t.Fatal(err)
			}
			restarted, err := New(ctx, a.Config, a.Log)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = restarted.Close() })
			cp, err := restarted.Collab.Save(ctx, repo, rev, maya, "Captured save")
			if err != nil || (intended != "" && cp.CommitSHA != intended) {
				t.Fatalf("resume exact save: %+v, %v", cp, err)
			}
			assertSaveJournalCounts(t, restarted.DB, id, 0, 1)
			if got := gitIn(t, remote, "show", rev.Branch+":docs/index.md"); got != "# Captured" {
				t.Fatalf("wrong content after recovery: %q", got)
			}
		})
	}
}

func TestSaveRecoveryRejectsExternalTipAndMissingPreparedCommit(t *testing.T) {
	for _, interruption := range []string{"external_tip", "corrupt_prepared_objects"} {
		t.Run(interruption, func(t *testing.T) {
			a, admin := newApp(t, nil)
			ctx := context.Background()
			maya, _ := users.Create(ctx, a.DB, "maya@example.org", "Maya", true)
			signIn(t, a, admin, maya)
			repoID, remote := connectLocalRemote(t, a, admin, map[string]string{"docs/index.md": "# Initial\n"})
			_, body := admin.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Interrupted save", "path": "docs/index.md"})
			id := body["id"].(string)
			runJobs(t, a)
			repo, _ := repos.Get(ctx, a.DB, repoID)
			rev, _ := revisions.Get(ctx, a.DB, id)
			if err := a.Collab.Apply(ctx, repo, rev, revisions.Caller{User: maya, Role: access.Admin}, "docs/index.md", "# Intended\n", "human"); err != nil {
				t.Fatal(err)
			}
			hook := filepath.Join(remote, ".git", "hooks", "pre-receive")
			if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			if _, err := a.Collab.Save(ctx, repo, rev, maya, "Interrupted"); err == nil {
				t.Fatal("save should fail before push")
			}
			if err := os.Remove(hook); err != nil {
				t.Fatal(err)
			}
			assertSaveJournalCounts(t, a.DB, id, 1, 0)
			expectedTip := rev.BranchSHA
			if interruption == "external_tip" {
				writeFile(t, remote, "outside.md", "An external commit\n")
				gitIn(t, remote, "add", "outside.md")
				gitIn(t, remote, "commit", "--quiet", "-m", "External writer")
				expectedTip = gitIn(t, remote, "rev-parse", "HEAD")
				gitIn(t, remote, "update-ref", "refs/heads/"+rev.Branch, expectedTip)
			} else {
				if err := os.RemoveAll(a.Repos.Mirror(repo).Path); err != nil {
					t.Fatal(err)
				}
				if _, err := store.Exec(ctx, a.DB, `UPDATE revision_save_intents SET commit_objects = ? WHERE revision_id = ?`, []byte("corrupted pack"), id); err != nil {
					t.Fatal(err)
				}
			}
			_, err := a.Collab.Save(ctx, repo, rev, maya, "Retry")
			if interruption == "external_tip" && !errors.Is(err, branches.ErrBranchMoved) {
				t.Fatalf("external tip was adopted: %v", err)
			}
			if interruption == "corrupt_prepared_objects" && (err == nil || !strings.Contains(err.Error(), "saved Git objects")) {
				t.Fatalf("missing local commit not reported: %v", err)
			}
			if interruption == "corrupt_prepared_objects" {
				if code, body := admin.do("POST", "/revisions/"+id+"/save", map[string]any{}); code != 409 || body["code"] != "save_commit_missing" {
					t.Fatalf("missing save must give a recovery instruction: %d %v", code, body)
				}
			}
			if got := gitIn(t, remote, "rev-parse", rev.Branch); got != expectedTip {
				t.Fatalf("recovery overwrote remote history: %s", got)
			}
			assertSaveJournalCounts(t, a.DB, id, 1, 0)
		})
	}
}

func TestSaveRecoveryPreservesSnapshotAfterLiveDocumentDeletion(t *testing.T) {
	sqliteOnly(t)
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@example.org", "Maya", true)
	signIn(t, a, admin, maya)
	repoID, _ := connectLocalRemote(t, a, admin, map[string]string{"docs/index.md": "# Initial\n"})
	_, body := admin.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Recover deleted document", "path": "docs/index.md"})
	id := body["id"].(string)
	runJobs(t, a)
	repo, _ := repos.Get(ctx, a.DB, repoID)
	rev, _ := revisions.Get(ctx, a.DB, id)
	caller := revisions.Caller{User: maya, Role: access.Admin}
	if err := a.Collab.Apply(ctx, repo, rev, caller, "docs/index.md", "# Captured before deletion\n", "human"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Exec(ctx, a.DB, `CREATE TRIGGER fail_save BEFORE UPDATE OF branch_sha ON revisions BEGIN SELECT RAISE(FAIL, 'interrupted'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Collab.Save(ctx, repo, rev, maya, "Capture document"); err == nil {
		t.Fatal("save should fail after push")
	}
	var raw []byte
	if err := store.QueryRow(ctx, a.DB, `SELECT payload FROM revision_save_intents WHERE revision_id = ?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var captured struct {
		Files []struct {
			State []byte `json:"state"`
		} `json:"files"`
	}
	if err := json.Unmarshal(raw, &captured); err != nil || len(captured.Files) != 1 || len(captured.Files[0].State) == 0 {
		t.Fatalf("missing captured snapshot: %s, %v", raw, err)
	}
	if _, err := store.Exec(ctx, a.DB, `DROP TRIGGER fail_save`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Revisions.ApplyFileOp(ctx, repo, rev, caller, revisions.FileOp{Op: revisions.OpDelete, Path: "docs/index.md"}); err != nil {
		t.Fatal(err)
	}
	cp, err := a.Collab.Save(ctx, repo, rev, maya, "Retry")
	if err != nil {
		t.Fatal(err)
	}
	var state []byte
	if err := store.QueryRow(ctx, a.DB, `SELECT ydoc_state FROM revision_checkpoint_files WHERE checkpoint_id = ?`, cp.ID).Scan(&state); err != nil || !bytes.Equal(state, captured.Files[0].State) {
		t.Fatalf("live document deletion lost saved snapshot: %v", err)
	}
	if dirty, err := revisions.Unsaved(ctx, a.DB, id); err != nil || !dirty {
		t.Fatalf("later deletion must remain unsaved: %v, %v", dirty, err)
	}
}

func TestSaveRejectsFailedMaterialization(t *testing.T) {
	sqliteOnly(t)
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@example.org", "Maya", true)
	signIn(t, a, admin, maya)
	repoID, remote := connectLocalRemote(t, a, admin, map[string]string{"docs/index.md": "# Initial\n"})
	_, body := admin.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Failed materialization", "path": "docs/index.md"})
	id := body["id"].(string)
	runJobs(t, a)
	repo, _ := repos.Get(ctx, a.DB, repoID)
	rev, _ := revisions.Get(ctx, a.DB, id)
	if _, err := store.Exec(ctx, a.DB, `CREATE TRIGGER fail_materialize BEFORE UPDATE OF content_md ON revision_files BEGIN SELECT RAISE(FAIL, 'interrupted'); END`); err != nil {
		t.Fatal(err)
	}
	if err := a.Collab.Apply(ctx, repo, rev, revisions.Caller{User: maya, Role: access.Admin}, "docs/index.md", "# Accepted edit\n", "human"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Collab.Save(ctx, repo, rev, maya, ""); err == nil {
		t.Fatal("save committed stale materialized content")
	}
	assertSaveJournalCounts(t, a.DB, id, 0, 0)
	if got := gitIn(t, remote, "rev-parse", rev.Branch); got != rev.BranchSHA {
		t.Fatalf("failed materialization changed branch: %s", got)
	}
	if _, err := store.Exec(ctx, a.DB, `DROP TRIGGER fail_materialize`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Collab.Save(ctx, repo, rev, maya, ""); err != nil {
		t.Fatalf("retry did not materialize accepted edit: %v", err)
	}
	if got := gitIn(t, remote, "show", rev.Branch+":docs/index.md"); got != "# Accepted edit" {
		t.Fatalf("retry saved stale content: %q", got)
	}
}
