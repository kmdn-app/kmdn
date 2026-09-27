package gitmirror

import (
	"context"
	"testing"
)

func TestPreparedCommitPackRestoresExactObjects(t *testing.T) {
	ctx := context.Background()
	remote := newRemote(t)
	remote.write("docs/index.md", "# Original\n")
	base := remote.commit("Initial")
	m := &Mirror{Git: &Git{}, Path: t.TempDir() + "/m.git", URL: remote.url(), Branch: "main"}
	if err := m.Init(ctx, nil); err != nil {
		t.Fatal(err)
	}
	sha, err := m.BuildCommit(ctx, base, []Change{{Path: "docs/new/page.md", Content: []byte("# Prepared\n")}}, "Exact save\n", Identity{Name: "Maya", Email: "maya@example.org"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := m.Git.run(ctx, m.Path, nil, nil, "cat-file", "commit", sha)
	if err != nil {
		t.Fatal(err)
	}
	pack, err := m.PackCommit(ctx, nil, sha, []string{base})
	if err != nil {
		t.Fatal(err)
	}
	rebuilt := &Mirror{Git: &Git{}, Path: t.TempDir() + "/rebuilt.git", URL: remote.url(), Branch: "main"}
	if err := rebuilt.Init(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if rebuilt.HasCommit(ctx, sha) {
		t.Fatal("prepared commit unexpectedly on remote")
	}
	if err := rebuilt.RestoreCommitPack(ctx, nil, sha, pack); err != nil {
		t.Fatal(err)
	}
	got, err := rebuilt.Git.run(ctx, rebuilt.Path, nil, nil, "cat-file", "commit", sha)
	if err != nil || string(got) != string(raw) {
		t.Fatalf("commit headers changed: %q %v", got, err)
	}
	if page, err := rebuilt.ReadFile(ctx, nil, sha, "docs/new/page.md"); err != nil || string(page) != "# Prepared\n" {
		t.Fatalf("tree/blob not restored: %q %v", page, err)
	}
	if err := rebuilt.RestoreCommitPack(ctx, nil, sha, []byte("broken")); err == nil {
		t.Fatal("accepted corrupt pack")
	}
	if err := rebuilt.RestoreCommitPack(ctx, nil, "0000000000000000000000000000000000000001", pack); err == nil {
		t.Fatal("accepted pack without intended commit")
	}
}
