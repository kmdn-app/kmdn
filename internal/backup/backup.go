// Package backup writes and restores kmdn backups: the database (SQLite
// online copy), uploads and a redacted config, as one .tar.zst or .tar.gz
// (docs/specs/13-operations.md#backup-and-restore). Mirrors are left out:
// they're re-cloned after a restore. Collaborative documents live in the
// database.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"gopkg.in/yaml.v3"

	"github.com/kmdn-app/kmdn/internal/config"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/version"
)

// Format is the archive layout version.
const Format = 1

// Manifest describes a backup (manifest.json, first in the archive).
type Manifest struct {
	Format    int       `json:"format"`
	Version   string    `json:"kmdn_version"`
	CreatedAt time.Time `json:"created_at"`
	// DB is "sqlite" (kmdn.db is included) or "" (--skip-db, e.g. Postgres
	// backed up with pg_dump).
	DB string `json:"db"`
	// Migration is the schema version of the included database.
	Migration int  `json:"migration,omitempty"`
	Uploads   int  `json:"uploads"`
	Config    bool `json:"config"`
	// Secrets: the config includes secret_key and passwords.
	Secrets bool `json:"secrets"`
}

// Options configure a backup.
type Options struct {
	Config     config.Config
	ConfigPath string // included (redacted) when set
	Out        string // .tar.zst (default) or .tar.gz / .tgz
	SkipDB     bool
	// IncludeSecrets keeps secret_key and passwords in the config copy.
	IncludeSecrets bool
}

// SQLitePath returns the file of a sqlite:// URL ("" for other databases).
func SQLitePath(url string) string {
	if p, ok := strings.CutPrefix(url, "sqlite://"); ok {
		return p
	}
	return ""
}

type archive struct {
	tw    *tar.Writer
	close func() error
}

func create(out string) (*archive, error) {
	f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	var w io.WriteCloser
	switch {
	case strings.HasSuffix(out, ".tar.gz"), strings.HasSuffix(out, ".tgz"):
		w = gzip.NewWriter(f)
	default:
		zw, err := zstd.NewWriter(f)
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		w = zw
	}
	tw := tar.NewWriter(w)
	return &archive{tw: tw, close: func() error {
		return errors.Join(tw.Close(), w.Close(), f.Sync(), f.Close())
	}}, nil
}

func (a *archive) add(name string, mode int64, size int64, r io.Reader) error {
	if err := a.tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: size, ModTime: time.Now(), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	_, err := io.Copy(a.tw, r)
	return err
}

func (a *archive) addFile(name, src string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	return a.add(name, 0o600, fi.Size(), f)
}

// redact blanks secret_key and every password in a YAML config.
func redact(b []byte) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(n.Content); i += 2 {
				k, v := n.Content[i], n.Content[i+1]
				if (k.Value == "secret_key" || k.Value == "password" || strings.HasSuffix(k.Value, "_secret")) && v.Kind == yaml.ScalarNode {
					v.Value, v.Tag, v.Style = "", "!!str", yaml.DoubleQuotedStyle
					continue
				}
				walk(v)
			}
			return
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(&doc)
	return yaml.Marshal(&doc)
}

// Backup writes a consistent backup of a running or stopped instance.
func Backup(ctx context.Context, o Options) (m Manifest, err error) {
	m = Manifest{Format: Format, Version: version.Get().Version, CreatedAt: time.Now().UTC(), Secrets: o.IncludeSecrets}
	dbPath := SQLitePath(o.Config.DB.URL)
	if !o.SkipDB && dbPath == "" {
		return m, errors.New("this instance uses Postgres: back it up with pg_dump, then run kmdn backup --skip-db for uploads and config")
	}
	if o.Out == "" {
		o.Out = "kmdn-backup-" + m.CreatedAt.Format("20060102-150405") + ".tar.zst"
	}
	a, err := create(o.Out)
	if err != nil {
		return m, err
	}
	defer func() {
		if cerr := a.close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(o.Out)
		}
	}()
	var snapshot string
	if !o.SkipDB {
		// VACUUM INTO copies the database from a single read transaction:
		// consistent while kmdn keeps writing.
		tmp, err := os.MkdirTemp("", "kmdn-backup-")
		if err != nil {
			return m, err
		}
		defer func() { _ = os.RemoveAll(tmp) }()
		snapshot = filepath.Join(tmp, "kmdn.db")
		db, err := store.Open(ctx, o.Config.DB.URL)
		if err != nil {
			return m, err
		}
		_, err = db.ExecContext(ctx, `VACUUM INTO ?`, snapshot)
		if err == nil {
			err = db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&m.Migration)
		}
		db.Close()
		if err != nil {
			return m, fmt.Errorf("copy the database: %w", err)
		}
		m.DB = "sqlite"
	}
	var cfg []byte
	if o.ConfigPath != "" {
		if cfg, err = os.ReadFile(o.ConfigPath); err != nil {
			return m, err
		}
		if !o.IncludeSecrets {
			if cfg, err = redact(cfg); err != nil {
				return m, fmt.Errorf("redact %s: %w", o.ConfigPath, err)
			}
		}
		m.Config = true
	}
	uploads := filepath.Join(o.Config.DataDir, "uploads")
	var files []string
	err = filepath.WalkDir(uploads, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return m, err
	}
	m.Uploads = len(files)
	mb, _ := json.MarshalIndent(m, "", "  ")
	if err := a.add("manifest.json", 0o600, int64(len(mb)), strings.NewReader(string(mb))); err != nil {
		return m, err
	}
	if snapshot != "" {
		if err := a.addFile("kmdn.db", snapshot); err != nil {
			return m, err
		}
	}
	if cfg != nil {
		if err := a.add("kmdn.yaml", 0o600, int64(len(cfg)), strings.NewReader(string(cfg))); err != nil {
			return m, err
		}
	}
	for _, p := range files {
		rel, _ := filepath.Rel(uploads, p)
		if err := a.addFile(path.Join("uploads", filepath.ToSlash(rel)), p); err != nil {
			return m, err
		}
	}
	return m, nil
}
