// Command fakeforge is a small stand-in for GitLab for end-to-end tests: the
// REST calls kmdn makes (projects, protected branches, hooks, merge
// requests) and git over HTTP (git http-backend), with a token.
//
//	fakeforge -listen 127.0.0.1:18190 -root /tmp/forge -token glpat-e2e
//
// Test helpers: POST /_fake/projects {"path": "acme/handbook", "files":
// {"docs/index.md": "..."}, "protected": false} seeds a project;
// GET /_fake/state lists projects (with their branch heads), hooks and
// merge requests.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/cgi"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

type project struct {
	ID        int64  `json:"id"`
	Path      string `json:"path_with_namespace"`
	Protected bool   `json:"protected"`
}

type hook struct {
	Project int64  `json:"project_id"`
	URL     string `json:"url"`
	Token   string `json:"token"`
}

type mergeRequest struct {
	IID            int64  `json:"iid"`
	Project        int64  `json:"project_id"`
	SourceBranch   string `json:"source_branch"`
	TargetBranch   string `json:"target_branch"`
	Title          string `json:"title"`
	Description    string `json:"description"`
	WebURL         string `json:"web_url"`
	State          string `json:"state"` // opened | merged | closed
	Draft          bool   `json:"draft"`
	MergeCommitSHA string `json:"merge_commit_sha,omitempty"`
}

func isDraft(title string) bool { return strings.HasPrefix(strings.ToLower(title), "draft:") }

type forge struct {
	root, token, base, gitBin string

	mu       sync.Mutex
	projects map[int64]*project
	byPath   map[string]*project
	hooks    []hook
	mrs      []mergeRequest
	nextID   int64
}

func (f *forge) git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ana", "GIT_AUTHOR_EMAIL=ana@northwind.dev", "GIT_COMMITTER_NAME=Ana", "GIT_COMMITTER_EMAIL=ana@northwind.dev", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

func (f *forge) bare(p string) string { return filepath.Join(f.root, p+".git") }

// seed creates a bare repository with one commit on main.
func (f *forge) seed(p string, files map[string]string, protected bool) (*project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if pr, ok := f.byPath[p]; ok {
		pr.Protected = protected
		return pr, nil
	}
	work, err := os.MkdirTemp("", "fakeforge-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(work) }()
	if _, err := f.git(work, "init", "-q", "-b", "main"); err != nil {
		return nil, err
	}
	for name, body := range files {
		fp := filepath.Join(work, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(fp, []byte(body), 0o644); err != nil {
			return nil, err
		}
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "Initial docs"}} {
		if _, err := f.git(work, args...); err != nil {
			return nil, err
		}
	}
	bare := f.bare(p)
	if err := os.MkdirAll(filepath.Dir(bare), 0o755); err != nil {
		return nil, err
	}
	if _, err := f.git(work, "clone", "-q", "--bare", "--no-local", work, bare); err != nil {
		return nil, err
	}
	for _, kv := range [][2]string{{"http.receivepack", "true"}, {"uploadpack.allowFilter", "true"}, {"uploadpack.allowAnySHA1InWant", "true"}} {
		if _, err := f.git(bare, "config", kv[0], kv[1]); err != nil {
			return nil, err
		}
	}
	f.nextID++
	pr := &project{ID: 1000 + f.nextID, Path: p, Protected: protected}
	f.projects[pr.ID] = pr
	f.byPath[p] = pr
	return pr, nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// project resolves /projects/{id or url-encoded path}.
func (f *forge) project(ref string) *project {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		return f.projects[id]
	}
	p, _ := url.PathUnescape(ref)
	return f.byPath[p]
}

func (f *forge) api(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("PRIVATE-TOKEN") != f.token {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "401 Unauthorized"})
		return
	}
	// /api/v4/projects/{ref}[/{rest}]; the ref may contain an encoded slash.
	rest := strings.TrimPrefix(r.URL.EscapedPath(), "/api/v4/projects/")
	ref, sub, _ := strings.Cut(rest, "/")
	pr := f.project(ref)
	if pr == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "404 Project Not Found"})
		return
	}
	switch {
	case sub == "" && r.Method == http.MethodGet:
		name := pr.Path[strings.LastIndex(pr.Path, "/")+1:]
		writeJSON(w, http.StatusOK, map[string]any{"id": pr.ID, "path": name, "path_with_namespace": pr.Path, "default_branch": "main", "visibility": "private",
			"http_url_to_repo": f.base + "/" + pr.Path + ".git", "web_url": f.base + "/" + pr.Path, "last_activity_at": "2026-09-01T10:00:00Z"})
	case sub == "protected_branches" && r.Method == http.MethodGet:
		list := []any{}
		if pr.Protected {
			list = append(list, map[string]any{"name": "main", "push_access_levels": []any{map[string]int{"access_level": 40}}})
		}
		writeJSON(w, http.StatusOK, list)
	case sub == "hooks" && r.Method == http.MethodPost:
		var h hook
		_ = json.NewDecoder(r.Body).Decode(&h)
		h.Project = pr.ID
		f.mu.Lock()
		f.hooks = append(f.hooks, h)
		f.mu.Unlock()
		writeJSON(w, http.StatusCreated, map[string]any{"id": len(f.hooks)})
	case sub == "merge_requests" && r.Method == http.MethodPost:
		var mr mergeRequest
		_ = json.NewDecoder(r.Body).Decode(&mr)
		f.mu.Lock()
		mr.IID, mr.Project, mr.State, mr.Draft = int64(len(f.mrs)+1), pr.ID, "opened", isDraft(mr.Title)
		mr.WebURL = fmt.Sprintf("%s/%s/-/merge_requests/%d", f.base, pr.Path, mr.IID)
		f.mrs = append(f.mrs, mr)
		f.mu.Unlock()
		writeJSON(w, http.StatusCreated, mr)
	case sub == "merge_requests" && r.Method == http.MethodGet:
		q := r.URL.Query()
		list := []mergeRequest{}
		f.mu.Lock()
		for _, mr := range f.mrs {
			if mr.Project == pr.ID && (q.Get("source_branch") == "" || mr.SourceBranch == q.Get("source_branch")) && (q.Get("state") == "" || mr.State == q.Get("state")) {
				list = append(list, mr)
			}
		}
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, list)
	case strings.HasPrefix(sub, "merge_requests/"):
		f.mergeRequest(w, r, pr, strings.TrimPrefix(sub, "merge_requests/"))
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "404 Not Found"})
	}
}

// mergeRequest serves GET and PUT (title) on one merge request, and PUT .../merge.
func (f *forge) mergeRequest(w http.ResponseWriter, r *http.Request, pr *project, rest string) {
	iid, _ := strconv.ParseInt(strings.TrimSuffix(rest, "/merge"), 10, 64)
	f.mu.Lock()
	defer f.mu.Unlock()
	var mr *mergeRequest
	for i := range f.mrs {
		if f.mrs[i].Project == pr.ID && f.mrs[i].IID == iid {
			mr = &f.mrs[i]
		}
	}
	if mr == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "404 Not Found"})
		return
	}
	if merge, _ := strings.CutSuffix(rest, "/merge"); merge != rest && r.Method == http.MethodPut {
		f.merge(w, r, pr, mr)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, mr)
	case http.MethodPut:
		var in struct {
			Title *string `json:"title"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Title != nil {
			mr.Title, mr.Draft = *in.Title, isDraft(*in.Title)
		}
		writeJSON(w, http.StatusOK, mr)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "405 Method Not Allowed"})
	}
}

// merge merges a merge request into its target with a merge commit, like
// GitLab's "Merge commit" method (f.mu is held).
func (f *forge) merge(w http.ResponseWriter, r *http.Request, pr *project, mr *mergeRequest) {
	var in struct {
		Message      string `json:"merge_commit_message"`
		SHA          string `json:"sha"`
		Squash       bool   `json:"squash"`
		RemoveSource bool   `json:"should_remove_source_branch"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	bare := f.bare(pr.Path)
	switch {
	case mr.State != "opened":
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "405 Method Not Allowed"})
		return
	case mr.Draft:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "405 Method Not Allowed"})
		return
	case in.Squash:
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "fakeforge doesn't squash"})
		return
	}
	src, err := f.git(bare, "rev-parse", "refs/heads/"+mr.SourceBranch)
	if err != nil {
		writeJSON(w, http.StatusNotAcceptable, map[string]string{"message": "Branch cannot be merged"})
		return
	}
	if in.SHA != "" && in.SHA != src {
		writeJSON(w, http.StatusConflict, map[string]string{"message": "SHA does not match HEAD of source branch"})
		return
	}
	target, _ := f.git(bare, "rev-parse", "refs/heads/"+mr.TargetBranch)
	tree, err := f.git(bare, "merge-tree", "--write-tree", target, src)
	if err != nil {
		writeJSON(w, http.StatusNotAcceptable, map[string]string{"message": "Branch cannot be merged"})
		return
	}
	msg := in.Message
	if msg == "" {
		msg = "Merge branch '" + mr.SourceBranch + "' into '" + mr.TargetBranch + "'"
	}
	cmd := exec.Command("git", "commit-tree", strings.Fields(tree)[0], "-p", target, "-p", src, "-F", "-")
	cmd.Dir = bare
	cmd.Stdin = strings.NewReader(msg)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=kmdn bot", "GIT_AUTHOR_EMAIL=bot@fakeforge.test", "GIT_COMMITTER_NAME=kmdn bot", "GIT_COMMITTER_EMAIL=bot@fakeforge.test", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.Output()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": err.Error()})
		return
	}
	sha := strings.TrimSpace(string(out))
	if _, err := f.git(bare, "update-ref", "refs/heads/"+mr.TargetBranch, sha, target); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"message": err.Error()})
		return
	}
	if in.RemoveSource {
		_, _ = f.git(bare, "update-ref", "-d", "refs/heads/"+mr.SourceBranch)
	}
	mr.State, mr.MergeCommitSHA = "merged", sha
	writeJSON(w, http.StatusOK, mr)
}

// gitHTTP serves clone, fetch and push with basic auth (oauth2:<token>).
func (f *forge) gitHTTP(w http.ResponseWriter, r *http.Request) {
	if user, pass, ok := r.BasicAuth(); !ok || pass != f.token || user == "" {
		w.Header().Set("WWW-Authenticate", `Basic realm="fakeforge"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	(&cgi.Handler{
		Path: f.gitBin,
		Args: []string{"http-backend"},
		Env:  []string{"GIT_PROJECT_ROOT=" + f.root, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=kmdn", "GIT_CONFIG_GLOBAL=/dev/null"},
	}).ServeHTTP(w, r)
}

func (f *forge) state(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	type projState struct {
		project
		Branches map[string]string `json:"branches"`
		Log      []string          `json:"log"`
	}
	out := []projState{}
	for _, pr := range f.projects {
		ps := projState{project: *pr, Branches: map[string]string{}}
		if refs, err := f.git(f.bare(pr.Path), "for-each-ref", "--format=%(refname:short) %(objectname)", "refs/heads"); err == nil {
			for _, l := range strings.Split(refs, "\n") {
				if name, sha, ok := strings.Cut(l, " "); ok {
					ps.Branches[name] = sha
				}
			}
		}
		if l, err := f.git(f.bare(pr.Path), "log", "--first-parent", "--format=%s%n%b%n--", "main"); err == nil {
			ps.Log = strings.Split(l, "\n--\n")
		}
		out = append(out, ps)
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": out, "hooks": f.hooks, "merge_requests": f.mrs})
}

func main() {
	listen := flag.String("listen", "127.0.0.1:18190", "address")
	root := flag.String("root", "", "where repositories live (default: a temp dir)")
	token := flag.String("token", "glpat-e2e", "the access token kmdn must send")
	flag.Parse()
	if *root == "" {
		d, err := os.MkdirTemp("", "fakeforge-root-")
		if err != nil {
			log.Fatal(err)
		}
		*root = d
	}
	gitBin, err := exec.LookPath("git")
	if err != nil {
		log.Fatal("git isn't on PATH")
	}
	f := &forge{root: *root, token: *token, base: "http://" + *listen, gitBin: gitBin, projects: map[int64]*project{}, byPath: map[string]*project{}, hooks: []hook{}, mrs: []mergeRequest{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/", f.api)
	mux.HandleFunc("POST /_fake/projects", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Path      string            `json:"path"`
			Files     map[string]string `json:"files"`
			Protected bool              `json:"protected"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || !strings.Contains(in.Path, "/") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "path is namespace/name"})
			return
		}
		pr, err := f.seed(in.Path, in.Files, in.Protected)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, pr)
	})
	mux.HandleFunc("GET /_fake/state", f.state)
	mux.HandleFunc("GET /_fake/file", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		ref := q.Get("ref")
		if ref == "" {
			ref = "main"
		}
		out, err := f.git(f.bare(q.Get("project")), "show", ref+":"+q.Get("path"))
		if err != nil {
			http.Error(w, out, http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(out))
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, ".git/") {
			f.gitHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})
	log.Printf("fakeforge listening on %s (repos in %s)", *listen, *root)
	log.Fatal(http.ListenAndServe(*listen, mux))
}
