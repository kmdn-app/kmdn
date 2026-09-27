package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/backup"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

func TestSaveRecoveryAfterBackupBeforePush(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@example.org", "Maya", true)
	signIn(t, a, admin, maya)
	repoID, remote := connectLocalRemote(t, a, admin, map[string]string{"docs/index.md": "# Initial\n"})
	_, body := admin.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Recover backup", "path": "docs/index.md"})
	id := body["id"].(string)
	runJobs(t, a)
	repo, _ := repos.Get(ctx, a.DB, repoID)
	rev, _ := revisions.Get(ctx, a.DB, id)
	caller := revisions.Caller{User: maya, Role: access.Admin}
	if err := a.Collab.Apply(ctx, repo, rev, caller, "docs/index.md", "# Intended save\n", "human"); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(remote, ".git", "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Collab.Save(ctx, repo, rev, maya, "Original backup save"); err == nil {
		t.Fatal("save should stop before push")
	}
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	var intended, checkpoint, intendedHash string
	if err := store.QueryRow(ctx, a.DB, `SELECT commit_sha, checkpoint_id, content_hash FROM revision_save_intents WHERE revision_id = ?`, id).Scan(&intended, &checkpoint, &intendedHash); err != nil {
		t.Fatal(err)
	}
	if err := a.Collab.Apply(ctx, repo, rev, caller, "docs/index.md", "# Newer edit\n", "human"); err != nil {
		t.Fatal(err)
	}
	a.Collab.FlushRevision(ctx, id)
	archive := filepath.Join(t.TempDir(), "save.tar.gz")
	if _, err := backup.Backup(ctx, backup.Options{Config: a.Config, Out: archive}); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := a.Config
	cfg.Server.Listen = "127.0.0.1:0"
	if _, err := backup.Restore(ctx, backup.RestoreOptions{Config: cfg, In: archive, Force: true}); err != nil {
		t.Fatal(err)
	}
	if a.Repos.Mirror(repo).Exists() {
		t.Fatal("restore must remove mirror cache")
	}
	restarted, err := New(ctx, cfg, a.Log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	cp, err := restarted.Collab.Save(ctx, repo, rev, maya, "Retry after restore")
	if err != nil {
		t.Fatalf("recover prepush save from built-in backup: %v", err)
	}
	if cp.CommitSHA != intended || cp.ID != checkpoint || cp.CreatedBy != maya.ID || cp.Name != "Original backup save" {
		t.Fatalf("changed prepared save: %+v", cp)
	}
	var content, restoredHash string
	var state []byte
	if err := store.QueryRow(ctx, restarted.DB, `SELECT f.content_md, f.ydoc_state, c.content_hash FROM revision_checkpoint_files f JOIN revision_checkpoints c ON c.id = f.checkpoint_id WHERE c.id = ?`, cp.ID).Scan(&content, &state, &restoredHash); err != nil || content != "# Intended save\n" || len(state) == 0 || restoredHash != intendedHash {
		t.Fatalf("restored checkpoint changed: %q %s %d bytes, %v", content, restoredHash, len(state), err)
	}
	if got := gitIn(t, remote, "rev-parse", rev.Branch); got != intended {
		t.Fatalf("wrong restored commit: %s", got)
	}
	if got := gitIn(t, remote, "log", "-1", "--format=%an|%ae|%s", intended); got != "Maya|maya@example.org|Original backup save" {
		t.Fatalf("changed author/message: %s", got)
	}
	if got := gitIn(t, remote, "show", intended+":docs/index.md"); got != "# Intended save" {
		t.Fatalf("committed newer edits during recovery: %q", got)
	}
	if dirty, err := revisions.Unsaved(ctx, restarted.DB, id); err != nil || !dirty {
		t.Fatalf("newer edits must stay unsaved: %v, %v", dirty, err)
	}
	assertSaveJournalCounts(t, restarted.DB, id, 0, 1)
}
