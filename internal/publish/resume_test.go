package publish

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kmdn-app/kmdn/internal/branches"
	"github.com/kmdn-app/kmdn/internal/forge"
	"github.com/kmdn-app/kmdn/internal/gitmirror"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/storetest"
)

type claimForge struct {
	forge.Adapter
	merge  func() (forge.MergeResult, error)
	status func() (forge.ChangeRequestStatus, error)
}

func (*claimForge) OpenChangeRequest(context.Context, forge.Repo, forge.ChangeRequestInput) (forge.ChangeRequest, error) {
	panic("unexpected open")
}
func (*claimForge) FindChangeRequest(context.Context, forge.Repo, string) (forge.ChangeRequest, bool, error) {
	panic("unexpected find")
}
func (*claimForge) SetDraft(context.Context, forge.Repo, forge.ChangeRequest, bool) error {
	panic("unexpected draft")
}
func (f *claimForge) MergeChangeRequest(context.Context, forge.Repo, forge.ChangeRequest, forge.MergeInput) (forge.MergeResult, error) {
	return f.merge()
}
func (f *claimForge) ChangeRequestStatus(context.Context, forge.Repo, string) (forge.ChangeRequestStatus, error) {
	return f.status()
}

func claimFixture(t *testing.T) (*Service, repos.Repo, revisions.Revision, claim) {
	t.Helper()
	ctx := context.Background()
	remote := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = remote
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		} else {
			return strings.TrimSpace(string(out))
		}
		return ""
	}
	git("init", "-b", "main")
	git("config", "user.name", "Tester")
	git("config", "user.email", "tester@example.org")
	git("config", "receive.denyCurrentBranch", "updateInstead")
	if err := os.WriteFile(filepath.Join(remote, "README.md"), []byte("Initial\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "Initial")
	base := git("rev-parse", "HEAD")
	db := storetest.Open(t)
	for _, query := range []string{
		`INSERT INTO users (id, email, name, created_at) VALUES ('user_claim', 'claim@example.org', 'Publisher', 1)`,
		`INSERT INTO forge_hosts (id, kind, display_name, created_at) VALUES ('host_claim', 'git', 'Git', 1)`,
		`INSERT INTO repos (org_id, id, forge_host_id, owner, name, display_name, target_branch, created_at) VALUES ('org_default', 'repo_claim', 'host_claim', 'owner', 'repo', 'Repo', 'main', 1)`,
	} {
		if _, err := store.Exec(ctx, db, query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Exec(ctx, db, `UPDATE repos SET clone_url = ?, head_sha = ? WHERE id = 'repo_claim'`, remote, base); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Exec(ctx, db, `INSERT INTO revisions (id, repo_id, number, title, state, base_sha, created_at, updated_at, branch, branch_sha, branch_base_sha, change_request_url, change_request_ref) VALUES ('rev_claim', 'repo_claim', 1, 'Claim', 'approved', ?, 1, 1, 'kmdn/claim', ?, ?, 'https://forge.example/1', '1')`, base, base, base); err != nil {
		t.Fatal(err)
	}
	repo, err := repos.Get(ctx, db, "repo_claim")
	if err != nil {
		t.Fatal(err)
	}
	rev, _ := revisions.Get(ctx, db, "rev_claim")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rs := &repos.Service{DB: db, Git: &gitmirror.Git{}, DataDir: t.TempDir(), BaseURL: "https://docs.example", Log: log, Adapters: &repos.Adapters{DB: db}}
	if err := rs.Mirror(repo).Init(ctx, nil); err != nil {
		t.Fatal(err)
	}
	revisionsService := &revisions.Service{DB: db, Repos: rs, Log: log}
	s := &Service{DB: db, Repos: rs, Revisions: revisionsService, Branches: &branches.Service{DB: db, Repos: rs, Revisions: revisionsService, Log: log}, Log: log}
	c, err := s.createClaim(ctx, rev, repo, base, "", "Claim\n", "user_claim", nil)
	if err != nil {
		t.Fatal(err)
	}
	return s, repo, rev, c
}

func assertClaimActive(t *testing.T, s *Service, c claim) {
	t.Helper()
	rev, err := revisions.Get(context.Background(), s.DB, c.RevisionID)
	if err != nil || rev.State != revisions.Publishing {
		t.Fatalf("claim lost read-only state: %s %v", rev.State, err)
	}
	if _, ok, err := s.loadClaim(context.Background(), c.RevisionID); err != nil || !ok {
		t.Fatalf("claim lost: %v %v", ok, err)
	}
}

func TestDuplicateClaimRunnerCannotReleaseAmbiguousAttempt(t *testing.T) {
	s, repo, rev, c := claimFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	f := &claimForge{
		merge: func() (forge.MergeResult, error) {
			if calls.Add(1) == 1 {
				close(entered)
				<-release
				return forge.MergeResult{}, errors.New("connection lost after request")
			}
			return forge.MergeResult{}, forge.ErrNotMergeable
		},
		status: func() (forge.ChangeRequestStatus, error) {
			return forge.ChangeRequestStatus{Open: true, HeadSHA: c.BranchSHA}, nil
		},
	}
	results := make(chan error, 2)
	go func() { _, _, err := s.resumeClaim(context.Background(), repo, rev, c, f, nil); results <- err }()
	<-entered
	// Both callers started with the same stale 'claimed' phase.
	go func() { _, _, err := s.resumeClaim(context.Background(), repo, rev, c, f, nil); results <- err }()
	close(release)
	for range 2 {
		if err := <-results; err == nil {
			t.Fatal("uncertain merge unexpectedly succeeded")
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("merge calls: %d", calls.Load())
	}
	assertClaimActive(t, s, c)
}

func TestUnknownForgeOutcomeDoesNotReleaseClaim(t *testing.T) {
	for _, status := range []string{"locked", "unknown"} {
		t.Run(status, func(t *testing.T) {
			s, repo, rev, c := claimFixture(t)
			if err := s.claimPhase(context.Background(), c, "merging"); err != nil {
				t.Fatal(err)
			}
			f := &claimForge{status: func() (forge.ChangeRequestStatus, error) { return forge.ChangeRequestStatus{HeadSHA: c.BranchSHA}, nil }, merge: func() (forge.MergeResult, error) {
				t.Fatal("retried a merge with an unknown outcome")
				return forge.MergeResult{}, nil
			}}
			if _, _, err := s.resumeClaim(context.Background(), repo, rev, c, f, nil); err == nil {
				t.Fatal("unknown outcome should remain unresolved")
			}
			assertClaimActive(t, s, c)
		})
	}
}

func TestClaimCompletionRejectsDifferentSourceParent(t *testing.T) {
	s, repo, rev, c := claimFixture(t)
	ctx := context.Background()
	m := s.Repos.Mirror(repo)
	bot := gitmirror.Identity{Name: "Bot", Email: "bot@example.org"}
	other, err := m.BuildCommit(ctx, c.TargetSHA, []gitmirror.Change{{Path: "README.md", Content: []byte("Unapproved\n")}}, "Other", bot)
	if err != nil {
		t.Fatal(err)
	}
	merge, err := m.Commit(ctx, gitmirror.CommitInput{From: c.TargetSHA, Parents: []string{c.TargetSHA, other}, Message: "Merge unrelated source", Author: bot})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Push(ctx, nil, merge, "main", c.TargetSHA); err != nil {
		t.Fatal(err)
	}
	if err := s.finishClaim(ctx, repo, rev, c, merge); err == nil {
		t.Fatal("finalized a merge with a different source tip")
	}
	assertClaimActive(t, s, c)
}
