package doctor

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kmdn-app/kmdn/internal/config"
	"github.com/kmdn-app/kmdn/internal/llm"
	"github.com/kmdn-app/kmdn/internal/settings"
	"github.com/kmdn-app/kmdn/internal/store"
)

func aiCheck(t *testing.T, cfg config.Config, db *store.DB) Check {
	t.Helper()
	for _, c := range (&Doctor{Config: cfg, DB: db, Network: true}).Run(context.Background()) {
		if c.Name == "ai" {
			return c
		}
	}
	t.Fatal("no ai check")
	return Check{}
}

// The ai check sees the provider kmdn serve uses: the server config's
// (KMDN_ASSISTANT_*) wins over the console's, and its last capability check
// (run at startup) is reported.
func TestAIProviderFromConfig(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) }))
	defer provider.Close()
	ctx := context.Background()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.DB.URL = "sqlite://" + filepath.Join(dir, "kmdn.db")
	cfg.SecretKey = base64.StdEncoding.EncodeToString(make([]byte, 32))
	db, err := store.Open(ctx, cfg.DB.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	if c := aiCheck(t, cfg, db); c.Status != OK || !strings.Contains(c.Detail, "no AI provider") {
		t.Fatalf("no provider: %+v", c)
	}

	cfg.Assistant.Provider, cfg.Assistant.APIKey, cfg.Assistant.BaseURL, cfg.Assistant.Model = "anthropic", "sk-ant-test", provider.URL, "claude-sonnet-5"
	if c := aiCheck(t, cfg, db); c.Status != Warn || !strings.Contains(c.Detail, "from the server config") || !strings.Contains(c.Detail, "hasn't been checked yet") {
		t.Fatalf("before the startup check: %+v", c)
	}

	// What CheckEnv saves at startup: the console's settings (no provider) with the check.
	msg := "claude-sonnet-5 answered and called a tool."
	if err := settings.Set(ctx, db, "ai", llm.Settings{Check: &llm.Check{OK: true, Message: msg, At: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	c := aiCheck(t, cfg, db)
	if c.Status != OK || !strings.Contains(c.Detail, "anthropic (from the server config)") || !strings.Contains(c.Detail, provider.URL+" is reachable") || !strings.Contains(c.Detail, msg) {
		t.Fatalf("after the startup check: %+v", c)
	}

	if err := settings.Set(ctx, db, "ai", llm.Settings{Check: &llm.Check{Message: "invalid x-api-key", At: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	if c := aiCheck(t, cfg, db); c.Status != Warn || !strings.Contains(c.Detail, "invalid x-api-key") || !strings.Contains(c.Detail, "KMDN_ASSISTANT_*") {
		t.Fatalf("failed check: %+v", c)
	}

	cfg.Assistant.Enabled = false
	if c := aiCheck(t, cfg, db); c.Status != OK || !strings.Contains(c.Detail, "assistant.enabled is false") {
		t.Fatalf("disabled: %+v", c)
	}
}

// A forge host an org added is reached through the org client (the strict
// policy's guarded one), the instance's own through the plain one.
func TestForgeChecksUseTheOrgClient(t *testing.T) {
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer forge.Close()
	ctx := context.Background()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.DB.URL = "sqlite://" + filepath.Join(dir, "kmdn.db")
	cfg.SecretKey = base64.StdEncoding.EncodeToString(make([]byte, 32))
	db, err := store.Open(ctx, cfg.DB.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO orgs (id, slug, name, status, created_at) VALUES ('org_a', 'a', 'A', 'active', 0)`,
		`INSERT INTO forge_hosts (id, kind, base_url, api_url, display_name, org_id, created_at) VALUES ('fh_org', 'gitlab', ?, ?, 'Org GitLab', 'org_a', 0)`,
		`INSERT INTO forge_hosts (id, kind, base_url, api_url, display_name, created_at) VALUES ('fh_inst', 'gitlab', ?, ?, 'Instance GitLab', 0)`,
	} {
		if _, err := store.Exec(ctx, db, q, forge.URL, forge.URL+"/api/v4"); err != nil && !strings.Contains(q, "orgs") {
			t.Fatal(err)
		}
	}
	refuse := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) { return nil, errRefused })}
	got := map[string]string{}
	for _, c := range (&Doctor{Config: cfg, DB: db, Network: true, OrgHTTP: refuse}).Run(ctx) {
		got[c.Name] = c.Status
	}
	if got["forge: Org GitLab"] != Fail || got["forge: Instance GitLab"] != OK {
		t.Fatalf("forge checks: %v", got)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

var errRefused = errors.New("not a public address")
