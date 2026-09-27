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

// BuildCommit writes a commit on top of parent with changes applied, without
// touching any ref. It works in the partial clone: only the new blobs and
// trees are created; unchanged content is referenced by id.
func (m *Mirror) BuildCommit(ctx context.Context, parent string, changes []Change, message string, who Identity) (string, error) {
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
	if _, err := m.Git.runEnv(ctx, m.Path, nil, nil, env, "read-tree", parent); err != nil {
		return "", err
	}
	// --index-info works without a work tree (the mirror is bare); mode 0
	// removes an entry.
	var info strings.Builder
	for _, c := range changes {
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
	tree, err := m.Git.runEnv(ctx, m.Path, nil, nil, env, "write-tree")
	if err != nil {
		return "", err
	}
	idEnv := []string{
		"GIT_AUTHOR_NAME=" + who.Name, "GIT_AUTHOR_EMAIL=" + who.Email,
		"GIT_COMMITTER_NAME=" + who.Name, "GIT_COMMITTER_EMAIL=" + who.Email,
	}
	out, err := m.Git.runEnv(ctx, m.Path, nil, strings.NewReader(message), idEnv, "commit-tree", strings.TrimSpace(string(tree)), "-p", parent, "-F", "-")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
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
