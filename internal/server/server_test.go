package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kmdn-app/kmdn/internal/config"
)

func newTest(t *testing.T) *Server {
	t.Helper()
	return New(Options{
		Config: config.Defaults(),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		WebFS:  fstest.MapFS{"index.html": {Data: []byte("<html>spa</html>")}},
	})
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestHealthVersionAndSPA(t *testing.T) {
	s := newTest(t)
	if rec := get(t, s.Handler(), "/healthz"); rec.Code != 200 {
		t.Fatalf("healthz %d", rec.Code)
	}
	if rec := get(t, s.Handler(), "/version"); !strings.Contains(rec.Body.String(), `"version"`) {
		t.Fatalf("version body %s", rec.Body)
	}
	rec := get(t, s.Handler(), "/northwind/handbook")
	if rec.Body.String() != "<html>spa</html>" {
		t.Fatalf("spa fallback: %s", rec.Body)
	}
	if rec.Header().Get("Content-Security-Policy") == "" || rec.Header().Get("X-Request-Id") == "" {
		t.Fatal("missing security headers or request id")
	}
}

func TestReadyz(t *testing.T) {
	s := newTest(t)
	s.AddReadyCheck("db", func(context.Context) error { return errors.New("down") })
	if rec := get(t, s.Handler(), "/readyz"); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "down") {
		t.Fatalf("readyz: %d %s", rec.Code, rec.Body)
	}
}

func TestAPIMount(t *testing.T) {
	s := newTest(t)
	s.API().Get("/ping", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("pong")) })
	if rec := get(t, s.Handler(), "/api/v1/ping"); rec.Body.String() != "pong" {
		t.Fatalf("api mount: %s", rec.Body)
	}
}

func TestAPIUnknownIsJSON404(t *testing.T) {
	s := newTest(t)
	s.API().Get("/a", func(http.ResponseWriter, *http.Request) {})
	s.API().Get("/b", func(http.ResponseWriter, *http.Request) {})
	rec := get(t, s.Handler(), "/api/v1/nope")
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "not_found") {
		t.Fatalf("api 404: %d %s", rec.Code, rec.Body)
	}
}

func TestRecoverer(t *testing.T) {
	s := newTest(t)
	s.API().Get("/boom", func(http.ResponseWriter, *http.Request) { panic("boom") })
	if rec := get(t, s.Handler(), "/api/v1/boom"); rec.Code != 500 {
		t.Fatalf("recoverer: %d", rec.Code)
	}
}
