package gitmirror

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestBuildCommitAndPush(t *testing.T) {
	ctx := context.Background()
	rem := newRemote(t)
	rem.git("config", "receive.denyCurrentBranch", "updateInstead")
	rem.write("docs/a.md", "# A\n")
	rem.write("docs/b.md", "# B\n")
	rem.write("docs/big.md", strings.Repeat("unchanged\n", 1000))
	base := rem.commit("Initial")
	m := &Mirror{Git: &Git{}, Path: t.TempDir() + "/m.git", URL: rem.url(), Branch: "main"}
	if err := m.Init(ctx, nil); err != nil {
		t.Fatal(err)
	}
	msg := "Refresh docs\n\nKmdn-Revision: https://kmdn.example/r/1\nCo-authored-by: Tom <tom@x.dev>\n"
	sha, err := m.BuildCommit(ctx, base, []Change{
		{Path: "docs/a.md", Content: []byte("# A, better\n")},
		{Path: "docs/b.md", Delete: true},
		{Path: "docs/new/c.md", Content: []byte("# C\n")},
	}, msg, Identity{Name: "kmdn", Email: "kmdn@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Push(ctx, nil, sha, "main", base); err != nil {
		t.Fatalf("push: %v", err)
	}
	if got := rem.git("rev-parse", "HEAD"); got != sha {
		t.Fatalf("remote head %s, want %s", got, sha)
	}
	if got := rem.git("show", "HEAD:docs/a.md"); got != "# A, better" {
		t.Fatalf("a.md: %q", got)
	}
	if out := rem.git("ls-tree", "-r", "--name-only", "HEAD"); out != "docs/a.md\ndocs/big.md\ndocs/new/c.md" {
		t.Fatalf("tree: %q", out)
	}
	if got := rem.git("log", "-1", "--format=%an <%ae>|%s"); got != "kmdn <kmdn@example.test>|Refresh docs" {
		t.Fatalf("commit: %q", got)
	}
	// A stale lease is refused.
	again, _ := m.BuildCommit(ctx, base, []Change{{Path: "docs/a.md", Content: []byte("# A, other\n")}}, "Other\n", Identity{Name: "kmdn", Email: "kmdn@example.test"})
	if err := m.Push(ctx, nil, again, "main", base); !errors.Is(err, ErrStale) {
		t.Fatalf("stale push: %v", err)
	}
	// Resuming finds the landed commit by its trailer.
	if err := m.Fetch(ctx, nil); err != nil {
		t.Fatal(err)
	}
	head, _ := m.Head(ctx)
	if found, ok, err := m.FindTrailer(ctx, base, head, "Kmdn-Revision: https://kmdn.example/r/1"); err != nil || !ok || found != sha {
		t.Fatalf("find trailer: %s %v %v", found, ok, err)
	}
	if _, ok, _ := m.FindTrailer(ctx, base, head, "Kmdn-Revision: https://kmdn.example/r/2"); ok {
		t.Fatal("found a trailer that isn't there")
	}
	// A new branch (for a pull request) needs an empty lease.
	if err := m.Push(ctx, nil, again, "kmdn/2-other", ""); err != nil {
		t.Fatalf("branch push: %v", err)
	}
	if err := m.DeleteRemoteBranch(ctx, nil, "kmdn/2-other"); err != nil {
		t.Fatal(err)
	}
}
