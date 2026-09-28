package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

func TestBackupRestoreAndDoctor(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	cfgPath := filepath.Join(dir, "kmdn.yaml")
	var out bytes.Buffer
	if err := run([]string{"init", "-config", cfgPath}, &out, &out); err != nil {
		t.Fatal(err)
	}
	cfg, _ := os.ReadFile(cfgPath)
	cfg = []byte(strings.NewReplacer("./data/kmdn.db", data+"/kmdn.db", "data_dir: ./data", "data_dir: "+data, "listen: :8080", "listen: 127.0.0.1:1", `password: ""`, `password: "hunter2"`).Replace(string(cfg)))
	if err := os.WriteFile(cfgPath, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite://"+data+"/kmdn.db")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := users.Create(ctx, db, "maya@northwind.dev", "Maya", true); err != nil {
		t.Fatal(err)
	}
	db.Close()
	upload := filepath.Join(data, "uploads", "ab", "abcdef")
	_ = os.MkdirAll(filepath.Dir(upload), 0o750)
	_ = os.WriteFile(upload, []byte("png bytes"), 0o600)
	_ = os.MkdirAll(filepath.Join(data, "mirrors", "h", "r.git"), 0o750)

	// Doctor (offline) passes on a fresh instance.
	out.Reset()
	if err := run([]string{"doctor", "-offline", "-config", cfgPath}, &out, &out); err != nil {
		t.Fatalf("doctor: %v\n%s", err, out.String())
	}
	for _, want := range []string{"git ", "data_dir", "database", "migrations applied", "secret_key", "no failed jobs"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("doctor output lacks %q:\n%s", want, out.String())
		}
	}

	archive := filepath.Join(dir, "b.tar.zst")
	out.Reset()
	if err := run([]string{"backup", "-config", cfgPath, "-out", archive}, &out, &out); err != nil {
		t.Fatalf("backup: %v %s", err, out.String())
	}
	if !strings.Contains(out.String(), "1 upload(s)") {
		t.Fatalf("backup output: %s", out.String())
	}

	// A restore won't replace a database unless forced.
	if err := run([]string{"restore", "-config", cfgPath, "-in", archive}, &out, &out); err == nil || !strings.Contains(err.Error(), "-force") {
		t.Fatalf("restore over a database: %v", err)
	}
	for _, p := range []string{"kmdn.db", "kmdn.db-wal", "kmdn.db-shm", "uploads"} {
		_ = os.RemoveAll(filepath.Join(data, p))
	}
	out.Reset()
	if err := run([]string{"restore", "-config", cfgPath, "-in", archive}, &out, &out); err != nil {
		t.Fatalf("restore: %v %s", err, out.String())
	}
	db, err = store.Open(ctx, "sqlite://"+data+"/kmdn.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := users.ByEmail(ctx, db, "maya@northwind.dev"); err != nil {
		t.Fatalf("user not restored: %v", err)
	}
	if b, err := os.ReadFile(upload); err != nil || string(b) != "png bytes" {
		t.Fatalf("upload not restored: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(data, "mirrors")); !os.IsNotExist(err) {
		t.Fatal("mirrors kept (they must be re-cloned)")
	}
	copyCfg, err := os.ReadFile(filepath.Join(data, "kmdn.yaml.restored"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(copyCfg), "hunter2") || strings.Contains(string(copyCfg), string(cfg[strings.Index(string(cfg), "secret_key: ")+12:][:20])) {
		t.Fatalf("secrets in the config copy:\n%s", copyCfg)
	}
	if !strings.Contains(string(copyCfg), "secret_key: \"\"") {
		t.Fatalf("secret_key not blanked:\n%s", copyCfg)
	}

	// gzip works too, and Postgres instances must skip the database.
	if err := run([]string{"backup", "-config", cfgPath, "-out", filepath.Join(dir, "b.tgz")}, &out, &out); err != nil {
		t.Fatalf("gzip backup: %v", err)
	}
	t.Setenv("KMDN_DB_URL", "postgres://nobody@127.0.0.1:1/kmdn")
	if err := run([]string{"backup", "-config", cfgPath, "-out", filepath.Join(dir, "pg.tar.zst")}, &out, &out); err == nil || !strings.Contains(err.Error(), "pg_dump") {
		t.Fatalf("postgres backup without -skip-db: %v", err)
	}
	if err := run([]string{"backup", "-config", cfgPath, "-skip-db", "-out", filepath.Join(dir, "pg.tar.zst")}, &out, &out); err != nil {
		t.Fatalf("skip-db backup: %v", err)
	}
}

// After the database is restored some other way (Postgres from its own
// backups), resync-repos queues a sync of every repository, once each.
func TestResyncRepos(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "kmdn.yaml")
	var out bytes.Buffer
	if err := run([]string{"init", "-config", cfgPath}, &out, &out); err != nil {
		t.Fatal(err)
	}
	url := "sqlite://" + filepath.Join(dir, "kmdn.db")
	t.Setenv("KMDN_DB_URL", url)
	ctx := context.Background()
	db, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO forge_hosts (id, kind, display_name, created_at) VALUES ('fh_1', 'git', 'Git', 1)`,
		`INSERT INTO repos (org_id, id, forge_host_id, owner, name, display_name, target_branch, created_at) VALUES ('org_default', 'repo_a', 'fh_1', 'northwind', 'a', 'a', 'main', 1)`,
		`INSERT INTO repos (org_id, id, forge_host_id, owner, name, display_name, target_branch, created_at) VALUES ('org_default', 'repo_b', 'fh_1', 'northwind', 'b', 'b', 'main', 1)`,
	} {
		if _, err := store.Exec(ctx, db, q); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		out.Reset()
		if err := run([]string{"admin", "resync-repos", "-config", cfgPath}, &out, &out); err != nil || !strings.Contains(out.String(), "Queued a sync of 2 repositories") {
			t.Fatalf("resync: %v %s", err, out.String())
		}
	}
	var n int
	if err := store.QueryRow(ctx, db, `SELECT COUNT(*) FROM jobs WHERE kind = 'repo.sync' AND status = 'pending'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("queued syncs: %d %v", n, err)
	}
}
