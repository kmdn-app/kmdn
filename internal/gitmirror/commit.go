package gitmirror

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Change is one path in a new commit: new content, or a deletion.
type Change struct {
	Path    string
	Content []byte
	Delete  bool
}

// Identity is a commit's author and committer.
type Identity struct {
	Name  string
	Email string
}

// ErrStale is returned when the remote branch moved since lease (push rejected).
var ErrStale = errors.New("gitmirror: remote branch moved")

// CommitInput describes a commit to write.
type CommitInput struct {
	// From is the commit whose tree Changes apply to.
	From    string
	Changes []Change
	// Parents default to From. A merge lists more than one.
	Parents   []string
	Message   string
	Author    Identity
	Committer Identity // defaults to Author
}

// BuildCommit writes a commit on top of parent with changes applied, without
// touching any ref.
func (m *Mirror) BuildCommit(ctx context.Context, parent string, changes []Change, message string, who Identity) (string, error) {
	return m.Commit(ctx, CommitInput{From: parent, Changes: changes, Message: message, Author: who})
}

// Commit writes a commit without touching any ref. It works in the partial
// clone: only the new blobs and trees are created; unchanged content is
// referenced by id.
func (m *Mirror) Commit(ctx context.Context, in CommitInput) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx, err := os.CreateTemp("", "kmdn-index-*")
	if err != nil {
		return "", err
	}
	_ = idx.Close()
	_ = os.Remove(idx.Name()) // git creates it
	defer func() { _ = os.Remove(idx.Name()) }()
	env := []string{"GIT_INDEX_FILE=" + idx.Name()}
	if _, err := m.Git.runEnv(ctx, m.Path, nil, nil, env, "read-tree", in.From); err != nil {
		return "", err
	}
	// --index-info works without a work tree (the mirror is bare); mode 0
	// removes an entry.
	var info strings.Builder
	for _, c := range in.Changes {
		p := strings.TrimPrefix(filepath.ToSlash(c.Path), "/")
		if strings.ContainsAny(p, "\n\t") {
			return "", errors.New("gitmirror: path with a tab or newline")
		}
		if c.Delete {
			info.WriteString("0 0000000000000000000000000000000000000000\t" + p + "\n")
			continue
		}
		out, err := m.Git.run(ctx, m.Path, nil, bytes.NewReader(c.Content), "hash-object", "-w", "--stdin", "-t", "blob")
		if err != nil {
			return "", err
		}
		info.WriteString("100644 " + strings.TrimSpace(string(out)) + "\t" + p + "\n")
	}
	if _, err := m.Git.runEnv(ctx, m.Path, nil, strings.NewReader(info.String()), env, "update-index", "--index-info"); err != nil {
		return "", err
	}
	// --missing-ok: unchanged entries point at blobs the partial clone
	// never fetched (they're on the remote); only the new ones are local.
	tree, err := m.Git.runEnv(ctx, m.Path, nil, nil, env, "write-tree", "--missing-ok")
	if err != nil {
		return "", err
	}
	committer := in.Committer
	if committer.Name == "" {
		committer = in.Author
	}
	idEnv := []string{
		"GIT_AUTHOR_NAME=" + in.Author.Name, "GIT_AUTHOR_EMAIL=" + in.Author.Email,
		"GIT_COMMITTER_NAME=" + committer.Name, "GIT_COMMITTER_EMAIL=" + committer.Email,
	}
	parents := in.Parents
	if len(parents) == 0 {
		parents = []string{in.From}
	}
	args := []string{"commit-tree", strings.TrimSpace(string(tree))}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	out, err := m.Git.runEnv(ctx, m.Path, nil, strings.NewReader(in.Message), idEnv, append(args, "-F", "-")...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// KeepRef points a kmdn-private ref (refs/kmdn/...) at sha so the commit
// survives gc until kmdn drops it.
func (m *Mirror) KeepRef(ctx context.Context, ref, sha string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, err := m.Git.run(ctx, m.Path, nil, nil, "update-ref", ref, sha)
	return err
}

// HasCommit says whether the mirror holds commit sha.
func (m *Mirror) HasCommit(ctx context.Context, sha string) bool {
	if sha == "" {
		return false
	}
	_, err := m.Git.run(ctx, m.Path, nil, nil, "cat-file", "-e", sha+"^{commit}")
	return err == nil
}

// FetchBranch fetches a remote branch into a local ref and returns its tip
// (ErrNotFound when the remote has no such branch).
func (m *Mirror) FetchBranch(ctx context.Context, cred *Credential, branch, ref string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out, err := m.Git.run(ctx, m.Path, cred, nil, "ls-remote", "--heads", "origin", "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(out)) == "" {
		return "", ErrNotFound
	}
	if _, err := m.Git.run(ctx, m.Path, cred, nil, "fetch", "--quiet", "--no-tags", "--filter=blob:none", "origin", "+refs/heads/"+branch+":"+ref); err != nil {
		return "", err
	}
	tip, err := m.Git.run(ctx, m.Path, nil, nil, "rev-parse", "--verify", ref+"^{commit}")
	return strings.TrimSpace(string(tip)), err
}

// Push sets the remote branch to sha if it's still at lease ("" means the
// branch must not exist yet). A moved branch returns ErrStale.
func (m *Mirror) Push(ctx context.Context, cred *Credential, sha, branch, lease string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	out, err := m.Git.run(ctx, m.Path, cred, nil, "push", "--porcelain",
		"--force-with-lease=refs/heads/"+branch+":"+lease, "origin", sha+":refs/heads/"+branch)
	var ge *Error
	if errors.As(err, &ge) {
		// --porcelain reports per-ref status on stdout: "!\t<src>:<dst>\t[rejected] (stale info)".
		all := string(out) + "\n" + ge.Stderr
		if strings.Contains(all, "stale info") || strings.Contains(all, "[rejected]") || strings.Contains(all, "fetch first") || strings.Contains(all, "non-fast-forward") {
			return ErrStale
		}
	}
	return err
}

// DeleteRemoteBranch removes a branch on the remote (after a merged PR).
func (m *Mirror) DeleteRemoteBranch(ctx context.Context, cred *Credential, branch string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, err := m.Git.run(ctx, m.Path, cred, nil, "push", "--quiet", "origin", ":refs/heads/"+branch)
	return err
}

// FindTrailer returns the newest commit in from..to whose message contains
// the line (used to resume a publish that already landed).
func (m *Mirror) FindTrailer(ctx context.Context, from, to, line string) (string, bool, error) {
	rng := to
	if from != "" {
		rng = from + ".." + to
	}
	out, err := m.Git.run(ctx, m.Path, nil, nil, "log", "--format=%H", "-F", "--grep="+line, rng)
	if err != nil {
		return "", false, err
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return first, first != "", nil
}
