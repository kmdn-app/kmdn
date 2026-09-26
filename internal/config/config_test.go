package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func envMap(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

const key = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=" // 32 bytes

func TestLoadDefaultsWithoutFile(t *testing.T) {
	t.Chdir(t.TempDir())
	cfg, err := Load("", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != ":8080" || cfg.DB.URL != "sqlite://data/kmdn.db" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadFileAndEnvOverride(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "kmdn.yaml")
	mustWrite(t, p, ("server:\n  base_url: https://docs.example.com\n  listen: :9000\nsecret_key: ${MY_KEY}\nauth:\n  session_ttl: 48h\n"))
	cfg, err := Load(p, envMap(map[string]string{
		"MY_KEY":                      key,
		"KMDN_SERVER_LISTEN":          ":9100",
		"KMDN_AUTH_AUTO_JOIN_DOMAINS": "example.com, example.org",
		"KMDN_LIMITS_UPLOAD_MAX_MB":   "20",
		"KMDN_ASSISTANT_ENABLED":      "false",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.BaseURL != "https://docs.example.com" || cfg.Server.Listen != ":9100" {
		t.Fatalf("server: %+v", cfg.Server)
	}
	if cfg.SecretKey != key || cfg.Auth.SessionTTL != 48*time.Hour {
		t.Fatalf("expansion or duration failed: %+v", cfg)
	}
	if len(cfg.Auth.AutoJoinDomains) != 2 || cfg.Limits.UploadMaxMB != 20 || cfg.Assistant.Enabled {
		t.Fatalf("env overrides failed: %+v", cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownKeyRejected(t *testing.T) {
	p := filepath.Join(t.TempDir(), "kmdn.yaml")
	mustWrite(t, p, "servr:\n  listen: :1\n")
	if _, err := Load(p, envMap(nil)); err == nil {
		t.Fatal("expected error for unknown key")
	}
}

func TestExplicitMissingFileFails(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml"), envMap(nil)); err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateExplainsProblems(t *testing.T) {
	cfg := Defaults()
	cfg.Server.BaseURL = "docs.example.com"
	cfg.DB.URL = "mysql://x"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"server.base_url", "secret_key", "db.url"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
}

func TestEnvName(t *testing.T) {
	if got := EnvName("server.base_url"); got != "KMDN_SERVER_BASE_URL" {
		t.Fatal(got)
	}
}

func mustWrite(t *testing.T, p, body string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
