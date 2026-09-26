package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/kmdn-app/kmdn/internal/config"
)

func TestInitWritesValidConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "kmdn.yaml")
	var out bytes.Buffer
	if err := run([]string{"init", "-config", p}, &out, &out); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(p, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("generated config invalid: %v", err)
	}
	if err := run([]string{"init", "-config", p}, &out, &out); err == nil {
		t.Fatal("expected refusal to overwrite")
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("config should be 0600, got %v", fi.Mode().Perm())
	}
}

func TestUnknownCommand(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"nope"}, &out, &out); err == nil {
		t.Fatal("expected error")
	}
	if err := run([]string{"version"}, &out, &out); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateStatusAndUp(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KMDN_DATA_DIR", dir)
	t.Setenv("KMDN_DB_URL", "sqlite://"+filepath.Join(dir, "kmdn.db"))
	t.Chdir(dir)
	var out bytes.Buffer
	if err := run([]string{"migrate", "status"}, &out, &out); err != nil || !bytes.Contains(out.Bytes(), []byte("pending")) {
		t.Fatalf("status: %v %s", err, out.String())
	}
	out.Reset()
	if err := run([]string{"migrate", "up"}, &out, &out); err != nil || !bytes.Contains(out.Bytes(), []byte("Applied 2")) {
		t.Fatalf("up: %v %s", err, out.String())
	}
}
