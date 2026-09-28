package app

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/backup"
	"github.com/kmdn-app/kmdn/internal/collab"
	"github.com/kmdn-app/kmdn/internal/docengine"
	"github.com/kmdn-app/kmdn/internal/forge"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

func approvedClaimFixture(t *testing.T) (*App, *tc, repos.Repo, revisions.Revision, users.User, string) {
	t.Helper()
	a, client := newApp(t, nil)
	ctx := context.Background()
	u, err := users.Create(ctx, a.DB, "publisher@example.org", "Publisher", true)
	if err != nil {
		t.Fatal(err)
	}
	signIn(t, a, client, u)
	repoID, remote := connectLocalRemote(t, a, client, map[string]string{"docs/index.md": "# Initial\n"})
	_, body := client.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Claimed snapshot", "path": "docs/index.md"})
	id := body["id"].(string)
	repo, _ := repos.Get(ctx, a.DB, repoID)
	rev, _ := revisions.Get(ctx, a.DB, id)
	if err := a.Collab.Apply(ctx, repo, rev, revisions.Caller{User: u, Role: access.Admin}, "docs/index.md", "# Reviewed\n", "human"); err != nil {
		t.Fatal(err)
	}
	a.Collab.FlushRevision(ctx, id)
	if code, body := client.do("POST", "/revisions/"+id+"/submit", map[string]any{"reviewers": []string{u.ID}}); code != 200 {
		t.Fatalf("submit: %d %v", code, body)
	}
	if code, body := client.do("POST", "/revisions/"+id+"/approve", nil); code != 200 {
		t.Fatalf("approve: %d %v", code, body)
	}
	runJobs(t, a)
	rev, _ = revisions.Get(ctx, a.DB, id)
	return a, client, repo, rev, u, remote
}

func TestPublishClaimRejectsLateMutationsBeforeChangingDocuments(t *testing.T) {
	a, client, repo, stale, u, remote := approvedClaimFixture(t)
	ctx := context.Background()
	c := revisions.Caller{User: u, Role: access.Admin}
	_, before, _ := a.Collab.StateOf(ctx, stale.ID, "docs/index.md")
	beforeHash, _ := revisions.ContentHash(ctx, a.DB, stale.ID)
	changed := a.Revisions.Changed
	checked := false
	a.Revisions.Changed = func(ctx context.Context, rev revisions.Revision, kind string) {
		if kind == "publishing" {
			checked = true
			calls := map[string]func() error{
				"live content": func() error { return a.Collab.Apply(ctx, repo, stale, c, "docs/index.md", "# Late\n", "human") },
				"manifest": func() error {
					_, err := a.Revisions.ApplyFileOp(ctx, repo, stale, c, revisions.FileOp{Op: revisions.OpAdd, Path: "docs/late.md", Content: "# Late\n"})
					return err
				},
				"materialize": func() error {
					return a.Revisions.SetContent(ctx, stale.ID, "docs/index.md", "# Late\n", []string{u.ID})
				},
				"drop file": func() error { return a.Revisions.DropFile(ctx, stale, "docs/index.md") },
				"suggest": func() error {
					_, err := a.Collab.Suggest(ctx, repo, stale, c, "docs/index.md", "# Late\n", docengine.SuggestAttrs{ID: "late", Author: u.ID}, "assistant")
					return err
				},
				"resolve suggestions": func() error {
					_, err := a.Collab.ResolveSuggestions(ctx, repo, stale, c, collab.ResolveInput{Path: "docs/index.md", Action: "accept"})
					return err
				},
				"restore":          func() error { return a.Collab.Restore(ctx, repo, stale, c, "checkpoint") },
				"update":           func() error { _, err := a.Updates.Apply(ctx, stale, c, "pending"); return err },
				"resolve conflict": func() error { return a.Updates.ResolvePage(ctx, stale, c, "docs/index.md", "keep") },
				"asset": func() error {
					_, err := a.Revisions.Upload(ctx, repo, stale, c, "docs/index.md", "image.png", bytes.NewReader(nil))
					return err
				},
				"save":      func() error { _, err := a.Collab.Save(ctx, repo, stale, u, "Late save"); return err },
				"withdraw":  func() error { _, err := a.Revisions.Withdraw(ctx, stale, c); return err },
				"reviewers": func() error { return a.Revisions.RemoveReviewer(ctx, stale, c, u.ID) },
				"close":     func() error { _, err := a.Revisions.Close(ctx, stale, c); return err },
			}
			for name, call := range calls {
				var conflict *revisions.ErrConflict
				if err := call(); !errors.As(err, &conflict) || conflict.Code != "readonly" {
					t.Errorf("%s did not refuse at the claim boundary: %v", name, err)
				}
			}
			_, after, _ := a.Collab.StateOf(ctx, stale.ID, "docs/index.md")
			afterHash, _ := revisions.ContentHash(ctx, a.DB, stale.ID)
			if !bytes.Equal(before, after) || beforeHash != afterHash {
				t.Error("refused mutation changed accepted document or manifest")
			}
		}
		changed(ctx, rev, kind)
	}
	if code, body := client.do("POST", "/revisions/"+stale.ID+"/publish", map[string]any{}); code != 202 {
		t.Fatalf("publish: %d %v", code, body)
	}
	runJobs(t, a)
	if !checked {
		t.Fatal("publish claim was not reached")
	}
	if got := gitIn(t, remote, "show", "main:docs/index.md"); got != "# Reviewed" {
		t.Fatalf("published %q", got)
	}
}

func TestPublishClaimRecoversExactPushedMergeAfterRestart(t *testing.T) {
	sqliteOnly(t)
	a, client, repo, rev, u, remote := approvedClaimFixture(t)
	ctx := context.Background()
	if _, err := store.Exec(ctx, a.DB, `CREATE TRIGGER fail_publish BEFORE UPDATE OF state ON revisions WHEN NEW.state = 'published' BEGIN SELECT RAISE(FAIL, 'crash after push'); END`); err != nil {
		t.Fatal(err)
	}
	client.do("POST", "/revisions/"+rev.ID+"/publish", map[string]any{})
	runJobs(t, a)
	landed := gitIn(t, remote, "rev-parse", "main")
	var merge, tip string
	if err := store.QueryRow(ctx, a.DB, `SELECT merge_sha, branch_sha FROM revision_publish_claims WHERE revision_id = ?`, rev.ID).Scan(&merge, &tip); err != nil {
		t.Fatal(err)
	}
	if landed != merge || gitIn(t, remote, "rev-parse", "main^2") != tip {
		t.Fatalf("claim differs from actual merge: %s %s %s", landed, merge, tip)
	}
	current, _ := revisions.Get(ctx, a.DB, rev.ID)
	if current.State != revisions.Publishing {
		t.Fatalf("lost read-only claim: %s", current.State)
	}
	if err := a.Collab.Apply(ctx, repo, rev, revisions.Caller{User: u, Role: access.Admin}, "docs/index.md", "# Too late\n", "human"); err == nil {
		t.Fatal("accepted an edit after an uncertain publish result")
	}
	if _, err := store.Exec(ctx, a.DB, `DROP TRIGGER fail_publish`); err != nil {
		t.Fatal(err)
	}
	// Exhausted jobs must be recoverable after restart too.
	if _, err := store.Exec(ctx, a.DB, `UPDATE jobs SET status = 'failed' WHERE kind = 'revision.publish'`); err != nil {
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
	if err := restarted.Publish.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	runJobs(t, restarted)
	current, _ = revisions.Get(ctx, restarted.DB, rev.ID)
	if current.State != revisions.Published || current.PublishedSHA != landed {
		t.Fatalf("recovery did not finalize exact merge: %+v", current)
	}
	if got := gitIn(t, remote, "rev-parse", "main"); got != landed {
		t.Fatalf("recovery created another merge: %s", got)
	}
	var left int
	_ = store.QueryRow(ctx, restarted.DB, `SELECT COUNT(*) FROM revision_publish_claims WHERE revision_id = ?`, rev.ID).Scan(&left)
	if left != 0 {
		t.Fatal("completed claim remains active")
	}
}

func TestPublishClaimSurvivesBackupBeforePush(t *testing.T) {
	sqliteOnly(t)
	a, client, _, rev, _, remote := approvedClaimFixture(t)
	ctx := context.Background()
	initial := gitIn(t, remote, "rev-parse", "main")
	if _, err := store.Exec(ctx, a.DB, `CREATE TRIGGER fail_send BEFORE UPDATE OF phase ON revision_publish_claims BEGIN SELECT RAISE(FAIL, 'crash before push'); END`); err != nil {
		t.Fatal(err)
	}
	client.do("POST", "/revisions/"+rev.ID+"/publish", map[string]any{})
	runJobs(t, a)
	var intended string
	var objects []byte
	if err := store.QueryRow(ctx, a.DB, `SELECT merge_sha, merge_objects FROM revision_publish_claims WHERE revision_id = ?`, rev.ID).Scan(&intended, &objects); err != nil {
		t.Fatal(err)
	}
	if intended == "" || len(objects) == 0 {
		t.Fatal("claim did not persist the prepared merge objects")
	}
	if got := gitIn(t, remote, "rev-parse", "main"); got != initial {
		t.Fatal("test failure occurred after pushing")
	}
	if _, err := store.Exec(ctx, a.DB, `DROP TRIGGER fail_send`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Exec(ctx, a.DB, `UPDATE jobs SET status = 'failed' WHERE kind = 'revision.publish'`); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "pending-claim.tar.gz")
	if _, err := backup.Backup(ctx, backup.Options{Config: a.Config, Out: archive}); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := a.Config
	cfg.DataDir = t.TempDir()
	cfg.DB.URL = "sqlite://" + filepath.Join(cfg.DataDir, "kmdn.db")
	cfg.Server.Listen = "127.0.0.1:0"
	if _, err := backup.Restore(ctx, backup.RestoreOptions{Config: cfg, In: archive}); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(ctx, cfg, a.Log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	if err := restarted.Publish.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	runJobs(t, restarted)
	current, _ := revisions.Get(ctx, restarted.DB, rev.ID)
	if current.State != revisions.Published || current.PublishedSHA != intended {
		t.Fatalf("backup recovery changed or lost the claim: %+v, intended %s", current, intended)
	}
	if got := gitIn(t, remote, "rev-parse", "main"); got != intended {
		t.Fatalf("recovery published another commit: %s", got)
	}
}

func TestExternalMergeDoesNotCloseLaterLiveEdits(t *testing.T) {
	a, _, repo, rev, u, remote := approvedClaimFixture(t)
	ctx := context.Background()
	if err := a.Collab.Apply(ctx, repo, rev, revisions.Caller{User: u, Role: access.Admin}, "docs/index.md", "# Later accepted edit\n", "human"); err != nil {
		t.Fatal(err)
	}
	// The forge merges the saved branch while a newer edit remains in the room.
	gitIn(t, remote, "merge", "--no-ff", rev.Branch, "-m", "Manual merge")
	sha := gitIn(t, remote, "rev-parse", "main")
	if _, err := store.Exec(ctx, a.DB, `UPDATE revisions SET change_request_ref = '123' WHERE id = ?`, rev.ID); err != nil {
		t.Fatal(err)
	}
	var rejected bool
	for _, hook := range a.Repos.OnChangeRequest {
		if err := hook(ctx, repo, forge.ChangeRequestEvent{Ref: "123", Head: rev.Branch, Merged: true, MergeSHA: sha}); err != nil {
			rejected = true
		}
	}
	if !rejected {
		t.Fatal("external merge silently closed later accepted edits")
	}
	current, _ := revisions.Get(ctx, a.DB, rev.ID)
	if current.State == revisions.Published {
		t.Fatal("closed a revision whose accepted content was not merged")
	}
	file, _ := revisions.FileAt(ctx, a.DB, rev.ID, "docs/index.md")
	if file.ContentMD != "# Later accepted edit\n" {
		t.Fatalf("late content lost: %q", file.ContentMD)
	}
	if got := gitIn(t, remote, "show", "main:docs/index.md"); got != "# Reviewed" {
		t.Fatalf("manual merge changed expected saved snapshot: %q", got)
	}
}
