package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/config"
	"github.com/kmdn-app/kmdn/internal/storetest"
)

type tc struct {
	t    *testing.T
	base string
	c    *http.Client
}

func (c *tc) csrf() string {
	u, _ := url.Parse(c.base)
	for _, ck := range c.c.Jar.Cookies(u) {
		if ck.Name == auth.CSRFCookie {
			return ck.Value
		}
	}
	return ""
}

func (c *tc) do(method, path string, body any) (int, map[string]any) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, c.base+"/api/v1"+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if v := c.csrf(); v != "" {
		req.Header.Set(auth.CSRFHeader, v)
	}
	res, err := c.c.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func newApp(t *testing.T, mutate func(*config.Config)) (*App, *tc) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.DB.URL = "sqlite://" + filepath.Join(dir, "kmdn.db")
	if storetest.PostgresEnabled() {
		cfg.DB.URL = storetest.PostgresURL(t)
	}
	cfg.SecretKey = base64.StdEncoding.EncodeToString(make([]byte, 32))
	if mutate != nil {
		mutate(&cfg)
	}
	a, err := New(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	srv := httptest.NewServer(a.Server.Handler())
	t.Cleanup(srv.Close)
	return a, &tc{t: t, base: srv.URL, c: newClient()}
}

// sqliteOnly skips tests that simulate failures with SQLite triggers or back
// up the SQLite file.
func sqliteOnly(t *testing.T) {
	t.Helper()
	if storetest.PostgresEnabled() {
		t.Skip("SQLite only")
	}
}

func newClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

func TestSetupWizardFlow(t *testing.T) {
	a, c := newApp(t, nil)
	ctx := context.Background()
	code, st := c.do("GET", "/setup/status", nil)
	if code != 200 || st["needed"] != true || st["admin_exists"] != false {
		t.Fatalf("status: %d %v", code, st)
	}
	token, err := a.Setup.Prepare(ctx)
	if err != nil || token == "" {
		t.Fatalf("prepare: %q %v", token, err)
	}
	if code, body := c.do("POST", "/setup/admin", map[string]string{"token": "wrong", "name": "Maya", "email": "maya@northwind.dev"}); code != 403 {
		t.Fatalf("bad token: %d %v", code, body)
	}
	code, body := c.do("POST", "/setup/admin", map[string]string{"token": token, "name": "Maya Chen", "email": "Maya@Northwind.dev", "instance_name": "Northwind Docs"})
	if code != 201 || body["is_instance_admin"] != true {
		t.Fatalf("create admin: %d %v", code, body)
	}
	if code, _ := c.do("POST", "/setup/admin", map[string]string{"token": token, "name": "X", "email": "x@y.z"}); code != 409 && code != 403 {
		t.Fatalf("second admin: %d", code)
	}
	if code, me := c.do("GET", "/me", nil); code != 200 || me["email"] != "maya@northwind.dev" {
		t.Fatalf("me after setup: %d %v", code, me)
	}
	// SMTP must be configured before completing.
	if code, _ := c.do("POST", "/setup/complete", nil); code != 409 {
		t.Fatalf("complete without smtp: %d", code)
	}
	pw := "s3cret"
	if code, body := c.do("PUT", "/admin/smtp", map[string]any{"host": "log", "port": 587, "from": "Docs <docs@northwind.dev>", "username": "u", "password": pw}); code != 200 || body["has_password"] != true || body["configured"] != true {
		t.Fatalf("put smtp: %d %v", code, body)
	}
	if code, body := c.do("GET", "/admin/smtp", nil); code != 200 || strings.Contains(toJSON(body), pw) {
		t.Fatalf("password leaked or get failed: %d %v", code, body)
	}
	if code, body := c.do("POST", "/admin/smtp/test", map[string]any{}); code != 200 || body["ok"] != true {
		t.Fatalf("test email: %d %v", code, body)
	}
	if code, st := c.do("POST", "/setup/complete", nil); code != 200 || st["needed"] != false || st["instance_name"] != "Northwind Docs" {
		t.Fatalf("complete: %d %v", code, st)
	}
	// Prepare is a no-op once an admin exists.
	if tok, _ := a.Setup.Prepare(ctx); tok != "" {
		t.Fatal("setup token issued after setup")
	}
}

func TestSMTPLockedByConfigAndAdminOnly(t *testing.T) {
	_, c := newApp(t, func(cfg *config.Config) {
		cfg.SMTP.Host = "log"
		cfg.SMTP.From = "docs@example.com"
	})
	if code, _ := c.do("GET", "/admin/smtp", nil); code != 401 {
		t.Fatalf("anonymous admin access: %d", code)
	}
	code, st := c.do("GET", "/setup/status", nil)
	if code != 200 || st["smtp_configured"] != true || st["smtp_from_config"] != true {
		t.Fatalf("status: %v", st)
	}
}

func toJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
