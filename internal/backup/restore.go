package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
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

type archiveReader struct {
	*tar.Reader
	source io.Reader
}

func open(in string) (*archiveReader, func(), error) {
	f, err := os.Open(in)
	if err != nil {
		return nil, nil, err
	}
	var r io.Reader
	var closeAll func()
	switch {
	case strings.HasSuffix(in, ".tar.gz"), strings.HasSuffix(in, ".tgz"):
		gr, err := gzip.NewReader(f)
		if err != nil {
			_ = f.Close()
			return nil, nil, err
		}
		r = gr
		closeAll = func() { _ = gr.Close(); _ = f.Close() }
	default:
		zr, err := zstd.NewReader(f)
		if err != nil {
			_ = f.Close()
			return nil, nil, err
		}
		r = zr
		closeAll = func() { zr.Close(); _ = f.Close() }
	}
	return &archiveReader{Reader: tar.NewReader(r), source: r}, closeAll, nil
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
	defer func() { _ = os.Remove(tmp.Name()) }()
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
	staged, err := stageArchive(ctx, o.In)
	if err != nil {
		return out, err
	}
	defer func() { _ = os.RemoveAll(staged.dir) }()
	out.Manifest = staged.manifest
	m := out.Manifest
	dbPath := SQLitePath(o.Config.DB.URL)
	if m.DB == "sqlite" {
		if dbPath == "" {
			return out, errors.New("the backup holds a SQLite database but db.url isn't sqlite://")
		}
		if _, err := os.Stat(dbPath); err == nil {
			if !o.Force {
				return out, fmt.Errorf("%s exists: use -force to replace it", dbPath)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return out, err
		}
	}
	dbURL := o.Config.DB.URL
	if m.DB == "sqlite" {
		dbURL = "sqlite://" + filepath.Join(staged.dir, "kmdn.db")
	}
	out.Repos, err = prepareDatabase(ctx, dbURL, m)
	if err != nil {
		return out, err
	}
	if err := os.MkdirAll(o.Config.DataDir, 0750); err != nil {
		return out, err
	}
	root, err := os.OpenRoot(o.Config.DataDir)
	if err != nil {
		return out, err
	}
	defer func() { _ = root.Close() }()
	for _, name := range staged.files {
		if name == "kmdn.db" {
			continue
		}
		dst := name
		if name == "kmdn.yaml" {
			dst = "kmdn.yaml.restored"
		}
		if err := staged.install(root, name, dst); err != nil {
			return out, err
		}
		if name == "kmdn.yaml" {
			out.ConfigCopy = filepath.Join(o.Config.DataDir, dst)
		}
	}
	if m.DB == "sqlite" {
		f, err := os.Open(filepath.Join(staged.dir, "kmdn.db"))
		if err != nil {
			return out, err
		}
		err = writeFile(dbPath, f)
		_ = f.Close()
		if err != nil {
			return out, fmt.Errorf("restore the database: %w", err)
		}
		for _, suffix := range []string{"-wal", "-shm"} {
			if err := os.Remove(dbPath + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
				return out, err
			}
		}
	}
	// Cache removal happens only after the backup and restored database validate.
	if err := root.RemoveAll("mirrors"); err != nil {
		return out, err
	}
	return out, nil
}

func prepareDatabase(ctx context.Context, dbURL string, manifest Manifest) (int, error) {
	db, err := store.Open(ctx, dbURL)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	if manifest.DB == "sqlite" {
		var integrity string
		if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&integrity); err != nil {
			return 0, err
		}
		if integrity != "ok" {
			return 0, errors.New("backup database failed integrity validation")
		}
		var version int
		if err := db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version); err != nil {
			return 0, err
		}
		migrations, err := store.Migrations()
		if err != nil {
			return 0, err
		}
		if version != manifest.Migration || version > len(migrations) {
			return 0, errors.New("backup database schema does not match a supported manifest")
		}
	}
	if _, err := db.Migrate(ctx); err != nil {
		return 0, fmt.Errorf("migrate the restored database: %w", err)
	}
	return ResyncRepos(ctx, db)
}

// ResyncRepos queues a sync of every repository, which clones missing
// mirrors again: after a restore, the database knows repositories whose
// mirrors aren't on this disk. It returns how many it queued.
func ResyncRepos(ctx context.Context, db *store.DB) (int, error) {
	list, err := repos.List(ctx, db, nil, true)
	if err != nil {
		return 0, err
	}
	q := jobs.New(db, jobs.Options{})
	for _, r := range list {
		if _, err := q.Enqueue(ctx, db, repos.JobSync, map[string]string{"repo_id": r.ID}, jobs.EnqueueOptions{Key: r.ID}); err != nil {
			return 0, err
		}
	}
	return len(list), nil
}
