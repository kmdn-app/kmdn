package app

import (
	"context"
	"strconv"
	"strings"
	"testing"

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
