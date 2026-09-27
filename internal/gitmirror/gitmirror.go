// Package gitmirror manages kmdn's bare mirrors of connected repositories and
// reads trees, blobs, history and blame from them with the git CLI.
// See docs/specs/06-git-and-forges.md#mirrors.
//
// Credentials never touch disk or command lines: git is pointed at the kmdn
// binary as GIT_ASKPASS, and the token travels in the child's environment.
package gitmirror

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/kmdn-app/kmdn/internal/telemetry"
)

// Credential authenticates git over HTTPS. Empty means anonymous.
type Credential struct {
	Username string
	Password string
}

// AskpassEnv is set when the kmdn binary is invoked by git as GIT_ASKPASS.
const (
	AskpassEnv     = "KMDN_GIT_ASKPASS"
	askpassUserEnv = "KMDN_GIT_USERNAME"
	askpassPassEnv = "KMDN_GIT_PASSWORD"
)

// RunAskpass handles a GIT_ASKPASS invocation: it prints the username or
// password for git's prompt. main() calls it first and exits when it returns true.
func RunAskpass(args []string, out io.Writer) bool {
	if os.Getenv(AskpassEnv) != "1" {
		return false
	}
	prompt := strings.ToLower(strings.Join(args, " "))
	if strings.Contains(prompt, "username") {
		fmt.Fprintln(out, os.Getenv(askpassUserEnv))
	} else {
		fmt.Fprintln(out, os.Getenv(askpassPassEnv))
	}
	return true
}

// ErrNotFound means the path or revision does not exist.
var ErrNotFound = errors.New("gitmirror: not found")

// Git runs git commands.
type Git struct {
	Binary  string // default "git"
	Askpass string // path to the kmdn binary; default os.Executable()
	// AllowProtocols restricts git's transports (GIT_ALLOW_PROTOCOL, e.g.
	// "https:ssh"); empty keeps git's defaults.
	AllowProtocols string
}

func (g *Git) bin() string {
	if g.Binary != "" {
		return g.Binary
	}
	return "git"
}

func (g *Git) askpass() string {
	if g.Askpass != "" {
		return g.Askpass
	}
	p, _ := os.Executable()
	return p
}

// Version returns git's version, e.g. "2.45.1".
func (g *Git) Version(ctx context.Context) (string, error) {
	out, err := g.run(ctx, "", nil, nil, "version")
	if err != nil {
		return "", err
	}
	m := regexp.MustCompile(`\d+\.\d+(\.\d+)?`).FindString(string(out))
	return m, nil
}

// CheckVersion errors when git is older than 2.40 (merge-tree --write-tree).
func (g *Git) CheckVersion(ctx context.Context) error {
	v, err := g.Version(ctx)
	if err != nil {
		return fmt.Errorf("git is not available: %w", err)
	}
	parts := strings.Split(v, ".")
	maj, _ := strconv.Atoi(parts[0])
	minor := 0
	if len(parts) > 1 {
		minor, _ = strconv.Atoi(parts[1])
	}
	if maj < 2 || (maj == 2 && minor < 40) {
		return fmt.Errorf("git %s is too old; kmdn needs git 2.40 or later", v)
	}
	return nil
}

func (g *Git) run(ctx context.Context, dir string, cred *Credential, stdin io.Reader, args ...string) ([]byte, error) {
	return g.runEnv(ctx, dir, cred, stdin, nil, args...)
}

// subcommand is git's command in args, after global options (-c k=v, -C dir).
func subcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-c" || a == "-C":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			return a
		}
	}
	return "git"
}

func (g *Git) runEnv(ctx context.Context, dir string, cred *Credential, stdin io.Reader, env []string, args ...string) (out []byte, err error) {
	sub := subcommand(args)
	ctx, span := telemetry.Tracer().Start(ctx, "git "+sub, trace.WithAttributes(attribute.String("kmdn.git.command", sub)))
	start := time.Now()
	defer func() {
		telemetry.GitCommands.WithLabelValues(sub, telemetry.Outcome(err)).Observe(time.Since(start).Seconds())
		telemetry.End(span, err)
	}()
	cmd := exec.CommandContext(ctx, g.bin(), args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"LC_ALL=C",
	)
	if g.AllowProtocols != "" {
		cmd.Env = append(cmd.Env, "GIT_ALLOW_PROTOCOL="+g.AllowProtocols)
	}
	cmd.Env = append(cmd.Env, env...)
	if cred != nil && cred.Password != "" {
		cmd.Env = append(cmd.Env,
			"GIT_ASKPASS="+g.askpass(),
			AskpassEnv+"=1",
			askpassUserEnv+"="+cred.Username,
			askpassPassEnv+"="+cred.Password,
		)
	}
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if cred != nil && cred.Password != "" {
			msg = strings.ReplaceAll(msg, cred.Password, "***")
		}
		return stdout.Bytes(), &Error{Args: redact(args), Stderr: msg, Err: err}
	}
	return stdout.Bytes(), nil
}

// Error is a failed git command.
type Error struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	return fmt.Sprintf("git %s: %v: %s", strings.Join(e.Args, " "), e.Err, e.Stderr)
}
func (e *Error) Unwrap() error { return e.Err }

func redact(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if u := strings.Index(a, "://"); u > 0 {
			if at := strings.Index(a[u+3:], "@"); at > 0 {
				a = a[:u+3] + "***@" + a[u+3+at+1:]
			}
		}
		out[i] = a
	}
	return out
}

// Mirror is a bare clone tracking one branch of a remote.
type Mirror struct {
	Git    *Git
	Path   string // bare repository directory
	URL    string
	Branch string
	// Cred supplies credentials for commands that read file contents
	// (history with rename following, blame): the partial clone fetches
	// those blobs on demand. Optional (public or local remotes).
	Cred func(ctx context.Context) (*Credential, error)

	mu sync.Mutex // serializes fetches and ref updates
}

func (m *Mirror) credential(ctx context.Context) (*Credential, error) {
	if m.Cred == nil {
		return nil, nil
	}
	return m.Cred(ctx)
}

// Exists reports whether the bare repository is initialized.
func (m *Mirror) Exists() bool {
	_, err := os.Stat(filepath.Join(m.Path, "HEAD"))
	return err == nil
}

// Init creates the bare mirror (idempotent) and fetches the branch.
func (m *Mirror) Init(ctx context.Context, cred *Credential) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.Exists() {
		if err := os.MkdirAll(m.Path, 0o750); err != nil {
			return err
		}
		if _, err := m.Git.run(ctx, m.Path, nil, nil, "init", "--bare", "--quiet"); err != nil {
			return err
		}
		for _, kv := range [][2]string{{"remote.origin.url", m.URL}, {"remote.origin.promisor", "true"}, {"remote.origin.partialclonefilter", "blob:none"}, {"gc.auto", "0"}, {"core.logAllRefUpdates", "false"}} {
			if _, err := m.Git.run(ctx, m.Path, nil, nil, "config", kv[0], kv[1]); err != nil {
				return err
			}
		}
	}
	return m.fetchLocked(ctx, cred)
}

// Fetch updates the tracked branch from the remote.
func (m *Mirror) Fetch(ctx context.Context, cred *Credential) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.fetchLocked(ctx, cred)
}

func (m *Mirror) fetchLocked(ctx context.Context, cred *Credential) error {
	if _, err := m.Git.run(ctx, m.Path, nil, nil, "config", "remote.origin.url", m.URL); err != nil {
		return err
	}
	refspec := fmt.Sprintf("+refs/heads/%s:refs/heads/%s", m.Branch, m.Branch)
	_, err := m.Git.run(ctx, m.Path, cred, nil, "fetch", "--quiet", "--prune", "--no-tags", "--filter=blob:none", "origin", refspec)
	return err
}

// Head returns the commit sha of the tracked branch.
func (m *Mirror) Head(ctx context.Context) (string, error) {
	out, err := m.Git.run(ctx, m.Path, nil, nil, "rev-parse", "--verify", "--quiet", "refs/heads/"+m.Branch+"^{commit}")
	if err != nil {
		return "", ErrNotFound
	}
	return strings.TrimSpace(string(out)), nil
}

// Entry is a tree entry.
type Entry struct {
	Path string `json:"path"`
	Type string `json:"type"` // blob | tree | commit
	Mode string `json:"mode"`
	SHA  string `json:"sha"`
	Size int64  `json:"size"` // -1: unknown (listings skip it; see Tree)
}

// Tree lists entries recursively under prefix at rev ("" lists the whole tree).
func (m *Mirror) Tree(ctx context.Context, rev, prefix string) ([]Entry, error) {
	// No --long: sizes need the blobs, which a partial (blob:none) mirror
	// fetches lazily, and a listing must not download every file (nor ask
	// for credentials). Entry.Size is -1.
	args := []string{"ls-tree", "-r", "-t", "-z", rev}
	if prefix = strings.Trim(prefix, "/"); prefix != "" {
		args = append(args, "--", prefix+"/")
	}
	out, err := m.Git.run(ctx, m.Path, nil, nil, args...)
	if err != nil {
		return nil, notFoundIf(err)
	}
	var entries []Entry
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		tab := bytes.IndexByte(rec, '\t')
		if tab < 0 {
			continue
		}
		f := strings.Fields(string(rec[:tab]))
		if len(f) < 3 {
			continue
		}
		entries = append(entries, Entry{Mode: f[0], Type: f[1], SHA: f[2], Size: -1, Path: string(rec[tab+1:])})
	}
	return entries, nil
}

// ReadFile returns a file's content at rev. Blobs missing from the partial
// clone are fetched on demand by git (needs cred for private remotes).
func (m *Mirror) ReadFile(ctx context.Context, cred *Credential, rev, path string) ([]byte, error) {
	out, err := m.Git.run(ctx, m.Path, cred, nil, "cat-file", "blob", rev+":"+strings.TrimPrefix(path, "/"))
	if err != nil {
		return nil, notFoundIf(err)
	}
	return out, nil
}

// ReadBlobs reads many blobs by sha in one git process (used for indexing).
func (m *Mirror) ReadBlobs(ctx context.Context, cred *Credential, shas []string, fn func(sha string, content []byte) error) error {
	if len(shas) == 0 {
		return nil
	}
	// Prefetch missing blobs in one go, then stream them.
	var in bytes.Buffer
	for _, s := range shas {
		in.WriteString(s + "\n")
	}
	out, err := m.Git.run(ctx, m.Path, cred, bytes.NewReader(in.Bytes()), "cat-file", "--batch")
	if err != nil {
		return err
	}
	r := bufio.NewReader(bytes.NewReader(out))
	for {
		header, err := r.ReadString('\n')
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		f := strings.Fields(header)
		if len(f) == 2 && f[1] == "missing" {
			continue
		}
		if len(f) != 3 {
			return fmt.Errorf("gitmirror: bad batch header %q", header)
		}
		n, _ := strconv.Atoi(f[2])
		buf := make([]byte, n+1)
		if _, err := io.ReadFull(r, buf); err != nil {
			return err
		}
		if err := fn(f[0], buf[:n]); err != nil {
			return err
		}
	}
}

// Commit is one entry of history.
type Commit struct {
	SHA         string    `json:"sha"`
	Parents     []string  `json:"parents"`
	AuthorName  string    `json:"author_name"`
	AuthorEmail string    `json:"author_email"`
	Date        time.Time `json:"date"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	CoAuthors   []Person  `json:"co_authors"`
	ReviewedBy  []Person  `json:"reviewed_by"`
	Revision    string    `json:"revision,omitempty"` // Kmdn-Revision trailer
	Path        string    `json:"path,omitempty"`     // path at this commit (follows renames)
}

// Person is a name and email from a commit or trailer.
type Person struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Log returns up to n commits touching path (following renames), newest first.
// An empty path lists branch history.
func (m *Mirror) Log(ctx context.Context, rev, path string, n int) ([]Commit, error) {
	// Each record: \x1e fields(\x1f-separated) \x1d, then --name-only lines.
	// --first-parent: a page's published versions are the target branch's
	// own commits. Publishing merges a revision's branch, so the commits
	// saved on it stay in history (and in blame) without each becoming a
	// version of their own.
	args := []string{"log", "--first-parent", fmt.Sprintf("-n%d", n), "--format=%x1e%H%x1f%P%x1f%an%x1f%ae%x1f%aI%x1f%s%x1f%b%x1d", rev}
	var cred *Credential
	if path != "" {
		args = append(args, "--follow", "--name-only", "--", path)
		// --follow compares contents to find renames.
		var err error
		if cred, err = m.credential(ctx); err != nil {
			return nil, err
		}
	}
	out, err := m.Git.run(ctx, m.Path, cred, nil, args...)
	if err != nil {
		return nil, notFoundIf(err)
	}
	var commits []Commit
	for _, rec := range strings.Split(string(out), "\x1e") {
		if strings.TrimSpace(rec) == "" {
			continue
		}
		fieldsPart, names, _ := strings.Cut(rec, "\x1d")
		f := strings.SplitN(fieldsPart, "\x1f", 7)
		if len(f) < 7 {
			continue
		}
		date, _ := time.Parse(time.RFC3339, f[4])
		c := Commit{SHA: f[0], AuthorName: f[2], AuthorEmail: f[3], Date: date.UTC(), Title: f[5], Path: strings.TrimSpace(names)}
		if f[1] != "" {
			c.Parents = strings.Fields(f[1])
		}
		c.Body, c.CoAuthors, c.ReviewedBy, c.Revision = parseTrailers(strings.TrimRight(f[6], "\n"))
		c.AuthorName, c.AuthorEmail, c.CoAuthors = creditPeople(c.AuthorName, c.AuthorEmail, c.CoAuthors)
		commits = append(commits, c)
	}
	return commits, nil
}

// CommitWithPaths is a commit and the files it changed.
type CommitWithPaths struct {
	Commit
	Paths []string
}

// Recent returns the latest n commits on rev with their changed paths.
func (m *Mirror) Recent(ctx context.Context, rev string, n int) ([]CommitWithPaths, error) {
	out, err := m.Git.run(ctx, m.Path, nil, nil, "log", fmt.Sprintf("-n%d", n), "--name-only", "--no-renames",
		"--format=%x1e%H%x1f%P%x1f%an%x1f%ae%x1f%aI%x1f%s%x1f%b%x1d", rev)
	if err != nil {
		return nil, notFoundIf(err)
	}
	var res []CommitWithPaths
	for _, rec := range strings.Split(string(out), "\x1e") {
		if strings.TrimSpace(rec) == "" {
			continue
		}
		fieldsPart, names, _ := strings.Cut(rec, "\x1d")
		f := strings.SplitN(fieldsPart, "\x1f", 7)
		if len(f) < 7 {
			continue
		}
		date, _ := time.Parse(time.RFC3339, f[4])
		c := Commit{SHA: f[0], AuthorName: f[2], AuthorEmail: f[3], Date: date.UTC(), Title: f[5]}
		if f[1] != "" {
			c.Parents = strings.Fields(f[1])
		}
		c.Body, c.CoAuthors, c.ReviewedBy, c.Revision = parseTrailers(strings.TrimRight(f[6], "\n"))
		c.AuthorName, c.AuthorEmail, c.CoAuthors = creditPeople(c.AuthorName, c.AuthorEmail, c.CoAuthors)
		var paths []string
		for _, l := range strings.Split(names, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				paths = append(paths, l)
			}
		}
		res = append(res, CommitWithPaths{Commit: c, Paths: paths})
	}
	return res, nil
}

var trailerRe = regexp.MustCompile(`(?m)^([A-Za-z-]+):\s*(.+?)\s*$`)
var personRe = regexp.MustCompile(`^(.*?)\s*<([^>]+)>$`)

func parseTrailers(body string) (rest string, co, rev []Person, revision string) {
	// Empty slices, not nil: they encode as [] in the API.
	co, rev = []Person{}, []Person{}
	lines := strings.Split(body, "\n")
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	start := end
	for start > 0 && trailerRe.MatchString(lines[start-1]) {
		start--
	}
	if start == end || (start > 0 && strings.TrimSpace(lines[start-1]) != "") {
		return strings.TrimSpace(body), co, rev, ""
	}
	for _, l := range lines[start:end] {
		mm := trailerRe.FindStringSubmatch(l)
		key, val := strings.ToLower(mm[1]), mm[2]
		person := func() Person {
			if p := personRe.FindStringSubmatch(val); p != nil {
				return Person{Name: p[1], Email: p[2]}
			}
			return Person{Name: val}
		}
		switch key {
		case "co-authored-by":
			co = append(co, person())
		case "reviewed-by":
			rev = append(rev, person())
		case "kmdn-revision":
			revision = val
		}
	}
	return strings.TrimSpace(strings.Join(lines[:start], "\n")), co, rev, revision
}

// BlameLine is the last change for a line.
type BlameLine struct {
	Line   int    `json:"line"` // 1-based
	SHA    string `json:"sha"`
	Author string `json:"author"`
	Email  string `json:"email"`
	Time   int64  `json:"time"`
	// CoAuthors are the other people credited on the commit.
	CoAuthors []string `json:"co_authors"`
}

// IsBot reports whether a commit author is kmdn itself: the plain git
// identity "kmdn" or a GitHub App ("<slug>[bot]").
func IsBot(name string) bool {
	return name == "kmdn" || strings.HasSuffix(name, "[bot]")
}

// creditPeople moves the credit for a commit kmdn made (a publish: a
// squashed commit or a merge) to the people in its Co-authored-by trailers.
// They're ordered by how much of their content survives, so the first is
// the change's main author.
func creditPeople(name, email string, co []Person) (string, string, []Person) {
	if !IsBot(name) || len(co) == 0 {
		return name, email, co
	}
	return co[0].Name, co[0].Email, co[1:]
}

// Blame returns per-line attribution of path at rev.
func (m *Mirror) Blame(ctx context.Context, rev, path string) ([]BlameLine, error) {
	cred, err := m.credential(ctx)
	if err != nil {
		return nil, err
	}
	out, err := m.Git.run(ctx, m.Path, cred, nil, "blame", "--porcelain", rev, "--", path)
	if err != nil {
		return nil, notFoundIf(err)
	}
	type info struct {
		author, email string
		t             int64
	}
	meta := map[string]*info{}
	var lines []BlameLine
	var cur string
	var curLine int
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		l := sc.Text()
		if strings.HasPrefix(l, "\t") {
			in := meta[cur]
			lines = append(lines, BlameLine{Line: curLine, SHA: cur, Author: in.author, Email: in.email, Time: in.t})
			continue
		}
		f := strings.Fields(l)
		if len(f) >= 3 && len(f[0]) == 40 {
			cur = f[0]
			curLine, _ = strconv.Atoi(f[2])
			if meta[cur] == nil {
				meta[cur] = &info{}
			}
			continue
		}
		in := meta[cur]
		switch {
		case strings.HasPrefix(l, "author "):
			in.author = l[7:]
		case strings.HasPrefix(l, "author-mail "):
			in.email = strings.Trim(l[12:], "<>")
		case strings.HasPrefix(l, "author-time "):
			in.t, _ = strconv.ParseInt(l[12:], 10, 64)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	// Commits kmdn made credit the people in their trailers.
	var bots []string
	for sha, in := range meta {
		if IsBot(in.author) {
			bots = append(bots, sha)
		}
	}
	credit := map[string][]Person{}
	if len(bots) > 0 {
		out, err := m.Git.run(ctx, m.Path, nil, nil, append([]string{"show", "-s", "--format=%H%x00%B%x01"}, bots...)...)
		if err != nil {
			return nil, err
		}
		for _, rec := range strings.Split(string(out), "\x01") {
			sha, body, ok := strings.Cut(strings.TrimSpace(rec), "\x00")
			if !ok {
				continue
			}
			_, co, _, _ := parseTrailers(body)
			credit[sha] = co
		}
	}
	for i := range lines {
		l := &lines[i]
		name, email, co := creditPeople(l.Author, l.Email, credit[l.SHA])
		l.Author, l.Email, l.CoAuthors = name, email, []string{}
		if IsBot(meta[l.SHA].author) {
			for _, p := range co {
				l.CoAuthors = append(l.CoAuthors, p.Name)
			}
		}
	}
	return lines, nil
}

// ChangedPaths lists paths that differ between two commits.
func (m *Mirror) ChangedPaths(ctx context.Context, from, to string) ([]string, error) {
	out, err := m.Git.run(ctx, m.Path, nil, nil, "diff", "--name-only", "-z", "--no-renames", from, to)
	if err != nil {
		return nil, notFoundIf(err)
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// Size returns the mirror's disk usage in bytes.
func (m *Mirror) Size() int64 {
	var total int64
	_ = filepath.Walk(m.Path, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

func notFoundIf(err error) error {
	var ge *Error
	if errors.As(err, &ge) {
		s := ge.Stderr
		if strings.Contains(s, "does not exist") || strings.Contains(s, "Not a valid object name") || strings.Contains(s, "not a tree object") ||
			strings.Contains(s, "bad revision") || strings.Contains(s, "no such path") || strings.Contains(s, "unknown revision") || strings.Contains(s, "Not a valid") {
			return fmt.Errorf("%w: %s", ErrNotFound, s)
		}
	}
	return err
}
