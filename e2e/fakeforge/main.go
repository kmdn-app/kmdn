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
	IID          int64  `json:"iid"`
	Project      int64  `json:"project_id"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	WebURL       string `json:"web_url"`
}

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
	if _, err := f.git(work, "clone", "-q", "--bare", work, bare); err != nil {
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
		mr.IID, mr.Project = int64(len(f.mrs)+1), pr.ID
		mr.WebURL = fmt.Sprintf("%s/%s/-/merge_requests/%d", f.base, pr.Path, mr.IID)
		f.mrs = append(f.mrs, mr)
		f.mu.Unlock()
		writeJSON(w, http.StatusCreated, mr)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "404 Not Found"})
	}
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
		if l, err := f.git(f.bare(pr.Path), "log", "--format=%s%n%b%n--", "main"); err == nil {
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
