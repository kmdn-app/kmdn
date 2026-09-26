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
