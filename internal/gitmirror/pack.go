package gitmirror

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
)

// PackCommit preserves the exact prepared commit and its new trees/blobs.
// Excluded commits must be retained on the remote. The pack is not thin, so
// importing never depends on a delta base outside the pack itself.
func (m *Mirror) PackCommit(ctx context.Context, cred *Credential, sha string, exclude []string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var revisions strings.Builder
	revisions.WriteString(sha + "\n")
	for _, base := range exclude {
		if base != "" {
			revisions.WriteString("^" + base + "\n")
		}
	}
	pack, err := m.Git.run(ctx, m.Path, cred, strings.NewReader(revisions.String()), "pack-objects", "--stdout", "--revs")
	if err != nil {
		return nil, fmt.Errorf("pack prepared commit: %w", err)
	}
	return pack, nil
}

// RestoreCommitPack imports prepared objects after fetching their remote
// parents. It verifies the requested commit and tree before callers retain a
// private ref or push it. It never creates or moves a ref itself.
func (m *Mirror) RestoreCommitPack(ctx context.Context, cred *Credential, sha string, pack []byte) error {
	if len(pack) == 0 {
		return errors.New("prepared commit has no saved Git objects")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// index-pack verifies pack and object hashes. --strict also demands every
	// referenced unchanged blob locally, which a partial clone need not have.
	if _, err := m.Git.run(ctx, m.Path, cred, bytes.NewReader(pack), "index-pack", "--stdin"); err != nil {
		return fmt.Errorf("restore prepared commit objects: %w", err)
	}
	got, err := m.Git.run(ctx, m.Path, cred, nil, "rev-parse", "--verify", sha+"^{commit}")
	if err != nil {
		return fmt.Errorf("saved Git objects do not contain prepared commit %s: %w", sha, err)
	}
	if strings.TrimSpace(string(got)) != sha {
		return fmt.Errorf("saved Git objects resolve to a different commit than %s", sha)
	}
	if _, err := m.Git.run(ctx, m.Path, cred, nil, "cat-file", "-e", sha+"^{tree}"); err != nil {
		return fmt.Errorf("prepared commit tree is unavailable: %w", err)
	}
	return nil
}
