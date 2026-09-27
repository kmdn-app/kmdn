// Package blobs stores uploaded files by key (docs/specs/16-organizations.md#uploads).
// Keys are per org: the same bytes uploaded by two orgs are stored twice, so
// one org can't learn that another has a file.
package blobs

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrNotFound is returned for a key with no blob.
var ErrNotFound = errors.New("blobs: not found")

// Store keeps blobs by key.
type Store interface {
	// Put stores b under key; storing the same key again is a no-op.
	Put(ctx context.Context, key string, b []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
	// Stat reports a blob's size, or ErrNotFound.
	Stat(ctx context.Context, key string) (int64, error)
}

// DefaultOrg is the org whose uploads keep the layout from before orgs
// (<sha[:2]>/<sha>), so existing files and backups stay where they are.
const DefaultOrg = "org_default"

// Key is where an org's upload with this SHA-256 lives.
func Key(orgID, sha string) string {
	if orgID == "" || orgID == DefaultOrg {
		return sha[:2] + "/" + sha
	}
	return orgID + "/" + sha[:2] + "/" + sha
}

// FS stores blobs as files under Root (<data>/uploads).
type FS struct{ Root string }

func (f FS) path(key string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(key))
	if key == "" || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("blobs: bad key")
	}
	return filepath.Join(f.Root, clean), nil
}

// Put writes the blob atomically (temp file, then rename).
func (f FS) Put(_ context.Context, key string, b []byte) error {
	dst, err := f.path(key)
	if err != nil {
		return err
	}
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".upload-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op after the rename
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

func (f FS) Get(_ context.Context, key string) ([]byte, error) {
	p, err := f.path(key)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	return b, err
}

func (f FS) Delete(_ context.Context, key string) error {
	p, err := f.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (f FS) Stat(_ context.Context, key string) (int64, error) {
	p, err := f.path(key)
	if err != nil {
		return 0, err
	}
	st, err := os.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}
