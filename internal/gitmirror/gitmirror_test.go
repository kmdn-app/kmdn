package gitmirror

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// remote builds a non-bare repo to act as the "forge" and returns its URL.
type remote struct {
	t   *testing.T
	dir string
}

func newRemote(t *testing.T) *remote {
	t.Helper()
	dir := t.TempDir()
	r := &remote{t: t, dir: dir}
	r.git("init", "--quiet", "-b", "main")
	r.git("config", "user.name", "Priya Raman")
	r.git("config", "user.email", "priya@northwind.dev")
	r.git("config", "uploadpack.allowFilter", "true")
	r.git("config", "uploadpack.allowAnySHA1InWant", "true")
	return r
}

func (r *remote) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *remote) write(path, content string) {
	r.t.Helper()
	p := filepath.Join(r.dir, path)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *remote) commit(msg string) string {
	r.git("add", "-A")
	r.git("commit", "--quiet", "-m", msg)
	return r.git("rev-parse", "HEAD")
}

func (r *remote) url() string { return "file://" + r.dir }

func TestMirrorReadsTreeFilesHistoryAndBlame(t *testing.T) {
	ctx := context.Background()
	rem := newRemote(t)
	rem.write("docs/onboarding/first-week.md", "# First week\n\nWelcome.\n")
	rem.write("docs/policies/remote-work.md", "# Remote work\n")
	rem.write("src/main.go", "package main\n")
	c1 := rem.commit("Create handbook")
	rem.write("docs/onboarding/first-week.md", "# First week\n\nWelcome to Northwind.\n\nDay one.\n")
	c2 := rem.commit("Update onboarding for 2026\n\nRefresh the first week.\n\nKmdn-Revision: https://kmdn.example/r/1\nCo-authored-by: Tom Okafor <tom@northwind.dev>\nCo-authored-by: Sam Lindqvist <sam@northwind.dev>\nReviewed-by: Maya Chen <maya@northwind.dev>")
	rem.git("mv", "docs/policies/remote-work.md", "docs/policies/working-remotely.md")
	c3 := rem.commit("Rename remote work policy")

	g := &Git{}
	if err := g.CheckVersion(ctx); err != nil {
		t.Skip(err)
	}
	m := &Mirror{Git: g, Path: filepath.Join(t.TempDir(), "handbook.git"), URL: rem.url(), Branch: "main"}
	if err := m.Init(ctx, nil); err != nil {
		t.Fatal(err)
	}
	head, err := m.Head(ctx)
	if err != nil || head != c3 {
		t.Fatalf("head %s want %s (%v)", head, c3, err)
	}
	entries, err := m.Tree(ctx, head, "docs")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, e := range entries {
		if e.Type == "blob" {
			paths = append(paths, e.Path)
		}
	}
	if strings.Join(paths, ",") != "docs/onboarding/first-week.md,docs/policies/working-remotely.md" {
		t.Fatalf("tree: %v", paths)
	}
	b, err := m.ReadFile(ctx, nil, head, "docs/onboarding/first-week.md")
	if err != nil || !strings.Contains(string(b), "Day one.") {
		t.Fatalf("read: %q %v", b, err)
	}
	if _, err := m.ReadFile(ctx, nil, head, "docs/nope.md"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing file: %v", err)
	}

	log, err := m.Log(ctx, head, "docs/onboarding/first-week.md", 10)
	if err != nil || len(log) != 2 {
		t.Fatalf("log: %+v %v", log, err)
	}
	if log[0].SHA != c2 || log[0].Title != "Update onboarding for 2026" || log[0].Body != "Refresh the first week." {
		t.Fatalf("latest commit: %+v", log[0])
	}
	if len(log[0].CoAuthors) != 2 || log[0].CoAuthors[1].Name != "Sam Lindqvist" || log[0].ReviewedBy[0].Email != "maya@northwind.dev" || log[0].Revision != "https://kmdn.example/r/1" {
		t.Fatalf("trailers: %+v", log[0])
	}
	if log[1].SHA != c1 || log[0].Path != "docs/onboarding/first-week.md" {
		t.Fatalf("older commit: %+v", log[1])
	}
	renamed, err := m.Log(ctx, head, "docs/policies/working-remotely.md", 10)
	if err != nil || len(renamed) != 2 || renamed[1].Path != "docs/policies/remote-work.md" {
		t.Fatalf("follow renames: %+v %v", renamed, err)
	}

	blame, err := m.Blame(ctx, head, "docs/onboarding/first-week.md")
	if err != nil || len(blame) != 5 {
		t.Fatalf("blame: %+v %v", blame, err)
	}
	if blame[0].SHA != c1 || blame[2].SHA != c2 || blame[0].Author != "Priya Raman" {
		t.Fatalf("blame attribution: %+v", blame)
	}

	changed, err := m.ChangedPaths(ctx, c1, c3)
	if err != nil || len(changed) != 3 {
		t.Fatalf("changed: %v %v", changed, err)
	}

	// New commits arrive on fetch.
	rem.write("docs/new.md", "new\n")
	c4 := rem.commit("Add page")
	if err := m.Fetch(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if h, _ := m.Head(ctx); h != c4 {
		t.Fatalf("after fetch head %s want %s", h, c4)
	}

	var got []string
	err = m.ReadBlobs(ctx, nil, []string{entries[len(entries)-1].SHA, "0000000000000000000000000000000000000000"}, func(sha string, content []byte) error {
		got = append(got, string(content))
		return nil
	})
	if err != nil || len(got) != 1 || got[0] != "# Remote work\n" {
		t.Fatalf("read blobs: %q %v", got, err)
	}
}

func TestParseTrailersWithoutTrailers(t *testing.T) {
	body, co, rev, r := parseTrailers("Just a body.\nSecond line: not a trailer block because no blank line")
	// Non-nil so the API encodes [] rather than null.
	if co == nil || rev == nil || len(co)+len(rev) != 0 || r != "" || !strings.HasPrefix(body, "Just a body.") {
		t.Fatalf("%q %v %v %q", body, co, rev, r)
	}
}

func TestRedactsCredentialsInErrors(t *testing.T) {
	g := &Git{}
	_, err := g.run(context.Background(), t.TempDir(), &Credential{Username: "x-access-token", Password: "ghs_supersecret"}, nil, "ls-remote", "https://x-access-token:ghs_supersecret@127.0.0.1:1/repo.git")
	if err == nil || strings.Contains(err.Error(), "ghs_supersecret") {
		t.Fatalf("secret leaked or no error: %v", err)
	}
}

func TestAskpass(t *testing.T) {
	t.Setenv(AskpassEnv, "1")
	t.Setenv(askpassUserEnv, "x-access-token")
	t.Setenv(askpassPassEnv, "tok")
	var b strings.Builder
	if !RunAskpass([]string{"Username for 'https://github.com': "}, &b) || b.String() != "x-access-token\n" {
		t.Fatalf("%q", b.String())
	}
	b.Reset()
	RunAskpass([]string{"Password for 'https://x-access-token@github.com': "}, &b)
	if b.String() != "tok\n" {
		t.Fatalf("%q", b.String())
	}
}
