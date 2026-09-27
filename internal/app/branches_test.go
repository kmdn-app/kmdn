package app

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/collab"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/users"
)

func TestRevisionOpensBranch(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	repoID, remote := connectLocalRemote(t, a, admin, map[string]string{"docs/index.md": "# Handbook\n"})
	base := strings.TrimSpace(gitIn(t, remote, "rev-parse", "main"))

	_, body := admin.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Onboarding for 2026!"})
	runJobs(t, a)
	rev, err := revisions.Get(ctx, a.DB, body["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	want := "kmdn/" + strconv.Itoa(rev.Number) + "-onboarding-for-2026"
	if rev.Branch != want || rev.BranchSHA == "" || rev.BranchBaseSHA != base {
		t.Fatalf("branch %q sha %q base %q", rev.Branch, rev.BranchSHA, rev.BranchBaseSHA)
	}
	// The branch starts with an empty commit by Maya, committed by kmdn,
	// that links back to the revision.
	if got := strings.TrimSpace(gitIn(t, remote, "rev-parse", want)); got != rev.BranchSHA {
		t.Fatalf("remote branch at %s, want %s", got, rev.BranchSHA)
	}
	log := gitIn(t, remote, "log", "-1", "--format=%an|%cn|%P|%B", want)
	if !strings.HasPrefix(log, "Maya|kmdn|"+base+"|Start revision #") || !strings.Contains(log, "Kmdn-Revision: ") || !strings.Contains(log, "/revisions/"+strconv.Itoa(rev.Number)) {
		t.Fatalf("start commit: %s", log)
	}
	if diff := gitIn(t, remote, "diff", "--stat", base, want); diff != "" {
		t.Fatalf("start commit changes files: %s", diff)
	}
	// Plain git has no pull requests.
	if rev.ChangeRequestRef != "" {
		t.Fatalf("change request on plain git: %q", rev.ChangeRequestRef)
	}
	events, _ := revisions.Events(ctx, a.DB, rev.ID)
	found := false
	for _, e := range events {
		found = found || e.Kind == "branch_created"
	}
	if !found {
		t.Fatal("no branch_created event")
	}
	// Syncing again changes nothing.
	if err := a.Branches.Sync(ctx, rev.ID); err != nil {
		t.Fatal(err)
	}
	again, _ := revisions.Get(ctx, a.DB, rev.ID)
	if again.BranchSHA != rev.BranchSHA || again.Branch != rev.Branch {
		t.Fatalf("second sync moved the branch: %+v", again)
	}
}

// gitInErr runs git in dir and returns its error instead of failing.
func gitInErr(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// When Published moves under a revision (here without touching its pages,
// so the base fast-forwards), the next save merges the new base: the pull
// request only shows the revision's own changes.
func TestSaveMergesMovedBase(t *testing.T) {
	a, admin := newApp(t, nil)
	a.Collab.Options = collab.Options{FlushDelay: time.Millisecond, QuietPeriod: time.Millisecond}
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	repoID, remote := connectLocalRemote(t, a, admin, map[string]string{"docs/index.md": "# Handbook\n", "docs/other.md": "# Other\n"})
	_, body := admin.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Index"})
	revID := body["id"].(string)
	rev, _ := revisions.Get(ctx, a.DB, revID)
	repo, _ := repos.Get(ctx, a.DB, repoID)
	caller := revisions.Caller{User: maya, Role: access.Admin}
	if err := a.Collab.Apply(ctx, repo, rev, caller, "docs/index.md", "# Handbook\n\nNew intro.\n", "human"); err != nil {
		t.Fatal(err)
	}
	if code, b := admin.do("POST", "/revisions/"+revID+"/save", map[string]any{}); code != 201 {
		t.Fatalf("save: %d %v", code, b)
	}

	// Someone changes another page on main.
	writeFile(t, remote, "docs/other.md", "# Other\n\nChanged on main.\n")
	gitIn(t, remote, "commit", "--quiet", "-am", "Other on main")
	head := gitIn(t, remote, "rev-parse", "main")
	admin.do("POST", "/repos/"+repoID+"/refresh", nil)
	runJobs(t, a)
	if rev, _ = revisions.Get(ctx, a.DB, revID); rev.BaseSHA != head {
		t.Fatalf("base didn't fast-forward: %s, head %s", rev.BaseSHA, head)
	}
	// The pull request still merges: nothing to save for that alone.
	if _, r := admin.do("GET", "/revisions/"+revID, nil); r["unsaved_changes"] != false {
		t.Fatalf("fast-forward reported as unsaved: %v", r["unsaved_changes"])
	}
	if err := a.Collab.Apply(ctx, repo, rev, caller, "docs/index.md", "# Handbook\n\nNewer intro.\n", "human"); err != nil {
		t.Fatal(err)
	}
	if code, b := admin.do("POST", "/revisions/"+revID+"/save", map[string]any{}); code != 201 {
		t.Fatalf("second save: %d %v", code, b)
	}
	rev, _ = revisions.Get(ctx, a.DB, revID)
	parents := strings.Fields(gitIn(t, remote, "log", "-1", "--format=%P", rev.Branch))
	if len(parents) != 2 || parents[1] != head {
		t.Fatalf("parents %v, want the previous save and %s", parents, head)
	}
	if rev.BranchBaseSHA != head {
		t.Fatalf("branch base %s", rev.BranchBaseSHA)
	}
	if diff := gitIn(t, remote, "diff", "--name-only", "main..."+rev.Branch); diff != "docs/index.md" {
		t.Fatalf("pull request diff: %q", diff)
	}
	if got := gitIn(t, remote, "show", rev.Branch+":docs/other.md"); !strings.Contains(got, "Changed on main.") {
		t.Fatalf("the branch lost main's change: %q", got)
	}
}
