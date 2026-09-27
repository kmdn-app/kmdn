package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmdn-app/kmdn/internal/config"
)

func TestRestoreRejectsInvalidArchiveBeforeChangingData(t *testing.T) {
	for _, kind := range []string{"missing-upload", "missing-config", "duplicate", "truncated", "checksum", "traversal"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			cfg := config.Defaults()
			cfg.DataDir, cfg.Server.Listen = dir, "127.0.0.1:1"
			cfg.DB.URL = "sqlite://" + filepath.Join(dir, "kmdn.db")
			original := []string{"kmdn.db", "kmdn.db-wal", "kmdn.db-shm", "uploads/old", "mirrors/repo/HEAD"}
			for _, name := range original {
				p := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(p), 0750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte("original "+name), 0600); err != nil {
					t.Fatal(err)
				}
			}
			archivePath := filepath.Join(t.TempDir(), "invalid.tar.gz")
			a, err := create(archivePath)
			if err != nil {
				t.Fatal(err)
			}
			manifest := `{"format":1,"uploads":0}`
			if kind == "missing-upload" {
				manifest = `{"format":1,"uploads":1}`
			}
			if kind == "missing-config" {
				manifest = `{"format":1,"uploads":0,"config":true}`
			}
			if err := a.add("manifest.json", 0600, int64(len(manifest)), strings.NewReader(manifest)); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "duplicate":
				if err := a.add("manifest.json", 0600, int64(len(manifest)), strings.NewReader(manifest)); err != nil {
					t.Fatal(err)
				}
			case "truncated":
				if err := a.add("uploads/new", 0600, 100, strings.NewReader("short")); err != nil {
					t.Fatal(err)
				}
			case "traversal":
				if err := a.add("uploads/../outside", 0600, 1, strings.NewReader("x")); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.close(); err != nil && kind != "truncated" {
				t.Fatal(err)
			}
			if kind == "checksum" {
				data, err := os.ReadFile(archivePath)
				if err != nil {
					t.Fatal(err)
				}
				data[len(data)-8] ^= 0xff
				if err := os.WriteFile(archivePath, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Restore(context.Background(), RestoreOptions{Config: cfg, In: archivePath, Force: true}); err == nil {
				t.Fatal("invalid archive accepted")
			}
			for _, name := range original {
				data, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || string(data) != "original "+name {
					t.Errorf("changed %s: %q %v", name, data, err)
				}
			}
		})
	}
}

func TestRestoreRejectsMissingDatabase(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.DB.URL = "sqlite://" + filepath.Join(dir, "kmdn.db")
	cfg.Server.Listen = "127.0.0.1:1"
	archivePath := filepath.Join(t.TempDir(), "missing.tar.gz")
	a, err := create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	manifest := `{"format":1,"db":"sqlite","uploads":0}`
	if err := a.add("manifest.json", 0600, int64(len(manifest)), strings.NewReader(manifest)); err != nil {
		t.Fatal(err)
	}
	if err := a.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(context.Background(), RestoreOptions{Config: cfg, In: archivePath}); err == nil {
		t.Error("restore accepted archive without its promised database")
	}
	if _, err := os.Stat(filepath.Join(dir, "kmdn.db")); !os.IsNotExist(err) {
		t.Error("restore created a blank database from incomplete backup")
	}
}

func TestRestorePreservesExistingDatabaseOnCorruption(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	dbPath := filepath.Join(dir, "kmdn.db")
	cfg.DB.URL = "sqlite://" + dbPath
	cfg.Server.Listen = "127.0.0.1:1"
	if err := os.WriteFile(dbPath, []byte("original database"), 0600); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "broken.tar.gz")
	a, err := create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	manifest := `{"format":1,"db":"sqlite","uploads":0}`
	if err := a.add("manifest.json", 0600, int64(len(manifest)), strings.NewReader(manifest)); err != nil {
		t.Fatal(err)
	}
	bad := "corrupt replacement database"
	if err := a.add("kmdn.db", 0600, int64(len(bad)), strings.NewReader(bad)); err != nil {
		t.Fatal(err)
	}
	if err := a.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(context.Background(), RestoreOptions{Config: cfg, In: archivePath, Force: true}); err == nil {
		t.Fatal("restore should reject corrupt database")
	}
	b, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "original database" {
		t.Fatalf("failed restore overwrote existing database with %q", b)
	}
}
