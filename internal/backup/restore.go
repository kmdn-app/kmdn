package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/kmdn-app/kmdn/internal/config"
	"github.com/kmdn-app/kmdn/internal/jobs"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/store"
)

// RestoreOptions configure a restore.
type RestoreOptions struct {
	Config config.Config
	In     string
	// Force replaces an existing database.
	Force bool
}

// Restored says what a restore did.
type Restored struct {
	Manifest Manifest
	// ConfigCopy is where the backed-up config was written (never over the live one).
	ConfigCopy string
	// Repos are queued for a fresh clone of their mirrors.
	Repos int
}

// running reports whether a kmdn server answers on the configured address.
func running(listen string) bool {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	c := &http.Client{Timeout: time.Second}
	res, err := c.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode == http.StatusOK
}

func open(in string) (*tar.Reader, func(), error) {
	f, err := os.Open(in)
	if err != nil {
		return nil, nil, err
	}
	var r io.Reader
	closeAll := func() { _ = f.Close() }
	switch {
	case strings.HasSuffix(in, ".tar.gz"), strings.HasSuffix(in, ".tgz"):
		gr, err := gzip.NewReader(f)
		if err != nil {
			_ = f.Close()
			return nil, nil, err
		}
		r = gr
	default:
		zr, err := zstd.NewReader(f)
		if err != nil {
			_ = f.Close()
			return nil, nil, err
		}
		r = zr
		closeAll = func() { zr.Close(); _ = f.Close() }
	}
	return tar.NewReader(r), closeAll, nil
}

// writeFile streams r to dst through a temporary file in the same folder.
func writeFile(dst string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".restore-*")
	if err != nil {
		return err
	}
	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := errors.Join(tmp.Sync(), tmp.Close()); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// Restore unpacks a backup into the configured data dir and database. kmdn
// must be stopped. Mirrors are removed and re-cloned on the next start.
func Restore(ctx context.Context, o RestoreOptions) (Restored, error) {
	var out Restored
	if running(o.Config.Server.Listen) {
		return out, fmt.Errorf("kmdn is running on %s: stop it before restoring", o.Config.Server.Listen)
	}
	tr, closeIn, err := open(o.In)
	if err != nil {
		return out, err
	}
	defer closeIn()
	h, err := tr.Next()
	if err != nil || h.Name != "manifest.json" {
		return out, errors.New("this isn't a kmdn backup (no manifest.json first)")
	}
	if err := json.NewDecoder(tr).Decode(&out.Manifest); err != nil {
		return out, fmt.Errorf("read the manifest: %w", err)
	}
	m := out.Manifest
	if m.Format != Format {
		return out, fmt.Errorf("backup format %d isn't supported by this kmdn (format %d)", m.Format, Format)
	}
	dbPath := SQLitePath(o.Config.DB.URL)
	if m.DB == "sqlite" {
		if dbPath == "" {
			return out, errors.New("the backup holds a SQLite database but db.url isn't sqlite://")
		}
		if _, err := os.Stat(dbPath); err == nil && !o.Force {
			return out, fmt.Errorf("%s exists: use -force to replace it", dbPath)
		}
	}
	data := o.Config.DataDir
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return out, err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		name := path.Clean(h.Name)
		if strings.HasPrefix(name, "../") || strings.HasPrefix(name, "/") {
			return out, fmt.Errorf("unsafe path in backup: %s", h.Name)
		}
		switch {
		case name == "kmdn.db" && m.DB == "sqlite":
			for _, suffix := range []string{"-wal", "-shm"} {
				_ = os.Remove(dbPath + suffix)
			}
			if err := writeFile(dbPath, tr); err != nil {
				return out, fmt.Errorf("restore the database: %w", err)
			}
		case name == "kmdn.yaml":
			out.ConfigCopy = filepath.Join(data, "kmdn.yaml.restored")
			if err := writeFile(out.ConfigCopy, tr); err != nil {
				return out, err
			}
		case strings.HasPrefix(name, "uploads/"):
			if err := writeFile(filepath.Join(data, filepath.FromSlash(name)), tr); err != nil {
				return out, err
			}
		}
	}
	// Mirrors are caches of the forges: clone them again.
	if err := os.RemoveAll(filepath.Join(data, "mirrors")); err != nil {
		return out, err
	}
	db, err := store.Open(ctx, o.Config.DB.URL)
	if err != nil {
		return out, err
	}
	defer db.Close()
	if _, err := db.Migrate(ctx); err != nil {
		return out, fmt.Errorf("migrate the restored database: %w", err)
	}
	list, err := repos.List(ctx, db, nil, true)
	if err != nil {
		return out, err
	}
	q := jobs.New(db, jobs.Options{})
	for _, r := range list {
		if _, err := q.Enqueue(ctx, db, repos.JobSync, map[string]string{"repo_id": r.ID}, jobs.EnqueueOptions{Key: r.ID}); err != nil {
			return out, err
		}
	}
	out.Repos = len(list)
	return out, nil
}
