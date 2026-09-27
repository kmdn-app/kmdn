package backup

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/kmdn-app/kmdn/internal/ids"
)

type stagedBackup struct {
	dir      string
	manifest Manifest
	files    []string
}

// Stage every member before touching the destination, including the compressor footer.
func stageArchive(ctx context.Context, in string) (out *stagedBackup, err error) {
	tr, closeIn, err := open(in)
	if err != nil {
		return nil, err
	}
	defer closeIn()
	h, err := tr.Next()
	if err != nil || h.Name != "manifest.json" || h.Typeflag != tar.TypeReg || h.Size > 1<<20 {
		return nil, errors.New("this isn't a kmdn backup (no valid manifest.json first)")
	}
	var manifest Manifest
	if err := json.NewDecoder(tr).Decode(&manifest); err != nil {
		return nil, fmt.Errorf("read the manifest: %w", err)
	}
	if manifest.Format != Format || (manifest.DB != "" && manifest.DB != "sqlite") || manifest.Uploads < 0 {
		return nil, errors.New("unsupported or invalid backup manifest")
	}
	dir, err := os.MkdirTemp("", "kmdn-restore-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	out = &stagedBackup{dir: dir, manifest: manifest}
	seen := map[string]bool{"manifest.json": true}
	uploads := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		name := path.Clean(h.Name)
		if name != h.Name || !filepath.IsLocal(name) || strings.Contains(name, "\\") {
			return nil, fmt.Errorf("unsafe path in backup: %s", h.Name)
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate backup entry: %s", name)
		}
		seen[name] = true
		if h.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("unsupported backup entry: %s", name)
		}
		switch {
		case name == "kmdn.db" && manifest.DB == "sqlite":
		case name == "kmdn.yaml" && manifest.Config:
		case strings.HasPrefix(name, "uploads/"):
			uploads++
		default:
			return nil, fmt.Errorf("unexpected backup entry: %s", name)
		}
		if err := writeFile(filepath.Join(dir, filepath.FromSlash(name)), tr); err != nil {
			return nil, err
		}
		out.files = append(out.files, name)
	}
	if _, err := io.Copy(io.Discard, tr.source); err != nil {
		return nil, fmt.Errorf("validate compressed backup: %w", err)
	}
	if (manifest.DB == "sqlite" && !seen["kmdn.db"]) || (manifest.Config && !seen["kmdn.yaml"]) || uploads != manifest.Uploads {
		return nil, errors.New("backup entries do not match the manifest")
	}
	return out, nil
}

func (s *stagedBackup) install(root *os.Root, name, dst string) error {
	if err := root.MkdirAll(filepath.Dir(dst), 0750); err != nil {
		return err
	}
	src, err := os.Open(filepath.Join(s.dir, filepath.FromSlash(name)))
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	tmpName := ".restore-" + ids.New("file")
	tmp, err := root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(tmpName) }()
	_, copyErr := io.Copy(tmp, src)
	if err := errors.Join(copyErr, tmp.Sync(), tmp.Close()); err != nil {
		return err
	}
	return root.Rename(tmpName, dst)
}
