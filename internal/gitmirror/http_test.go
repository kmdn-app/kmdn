package gitmirror

import (
	"context"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain lets git call this test binary as GIT_ASKPASS, like kmdn itself.
func TestMain(m *testing.M) {
	if RunAskpass(os.Args[1:], os.Stdout) {
		return
	}
	os.Exit(m.Run())
}

// A partial (blob:none) mirror of a private HTTPS-like remote: listings must
// not need blobs, history and blame fetch them with credentials, and
// publishing writes a tree over blobs the mirror never fetched.
func TestPartialMirrorOverAuthenticatedHTTP(t *testing.T) {
	r := newRemote(t)
	r.write("docs/a.md", "# A\n\nhello\n")
	r.write("docs/other.md", "# Other\n")
	r.commit("one")
	r.git("mv", "docs/a.md", "docs/b.md")
	r.commit("rename")
	root := t.TempDir()
	bare := filepath.Join(root, "r.git")
	r.git("clone", "--quiet", "--bare", r.dir, bare)
	for _, kv := range [][2]string{{"http.receivepack", "true"}, {"uploadpack.allowFilter", "true"}, {"uploadpack.allowAnySHA1InWant", "true"}} {
		if out, err := exec.Command("git", "-C", bare, "config", kv[0], kv[1]).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	gitBin, _ := exec.LookPath("git")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if u, p, ok := req.BasicAuth(); !ok || u != "oauth2" || p != "s3cret" {
			w.Header().Set("WWW-Authenticate", `Basic realm="t"`)
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		(&cgi.Handler{Path: gitBin, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=t"}}).ServeHTTP(w, req)
	}))
	defer srv.Close()
	ctx := context.Background()
	cred := &Credential{Username: "oauth2", Password: "s3cret"}
	m := &Mirror{Git: &Git{}, Path: filepath.Join(t.TempDir(), "m.git"), URL: srv.URL + "/r.git", Branch: "main",
		Cred: func(context.Context) (*Credential, error) { return cred, nil }}
	if err := m.Init(ctx, cred); err != nil {
		t.Fatal(err)
	}
	head, err := m.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := m.Tree(ctx, head, "docs")
	if err != nil {
		t.Fatalf("tree without fetching blobs: %v", err)
	}
	var paths []string
	for _, e := range entries {
		if e.Type == "blob" {
			paths = append(paths, e.Path)
		}
	}
	if got := strings.Join(paths, ","); got != "docs/b.md,docs/other.md" {
		t.Fatalf("tree: %s", got)
	}
	log, err := m.Log(ctx, head, "docs/b.md", 10)
	if err != nil || len(log) != 2 {
		t.Fatalf("history across the rename: %v %+v", err, log)
	}
	if _, err := m.Blame(ctx, head, "docs/b.md"); err != nil {
		t.Fatalf("blame: %v", err)
	}
	sha, err := m.BuildCommit(ctx, head, []Change{{Path: "docs/c.md", Content: []byte("# C\n")}}, "Add c", Identity{Name: "kmdn", Email: "bot@kmdn.test"})
	if err != nil {
		t.Fatalf("build a commit over unfetched blobs: %v", err)
	}
	if err := m.Push(ctx, cred, sha, "main", head); err != nil {
		t.Fatalf("push: %v", err)
	}
	if out, _ := exec.Command("git", "-C", bare, "rev-parse", "main").Output(); strings.TrimSpace(string(out)) != sha {
		t.Fatalf("remote main is %s, want %s", out, sha)
	}
}
