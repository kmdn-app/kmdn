// Package server wires the HTTP surface: the embedded SPA, /api/v1, /ws,
// webhooks, health and version endpoints.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/config"
	"github.com/kmdn-app/kmdn/internal/telemetry"
	"github.com/kmdn-app/kmdn/internal/version"
	"github.com/kmdn-app/kmdn/internal/web"
)

// ReadyCheck reports whether a dependency is ready to serve traffic.
type ReadyCheck func(context.Context) error

// Server is the kmdn HTTP server.
type Server struct {
	cfg    config.Config
	log    *slog.Logger
	webFS  fs.FS
	router chi.Router
	api    chi.Router
	// content serves the content origin (server.content_base_url), by Host.
	content http.Handler

	mu     sync.RWMutex
	checks map[string]ReadyCheck
}

// Options configures a Server. WebFS defaults to the embedded SPA.
type Options struct {
	Config config.Config
	Logger *slog.Logger
	WebFS  fs.FS
}

func New(o Options) *Server {
	if o.WebFS == nil {
		o.WebFS = web.FS()
	}
	s := &Server{cfg: o.Config, log: o.Logger, webFS: o.WebFS, checks: map[string]ReadyCheck{}}
	s.router = s.routes()
	return s
}

// API returns the router mounted at /api/v1 so packages can register handlers.
func (s *Server) API() chi.Router { return s.api }

// Router is the root router, for an embedding program's own routes.
func (s *Server) Router() chi.Router { return s.router }

// Mount attaches a handler under a path prefix (e.g. /ws, /hooks, /mcp).
func (s *Server) Mount(pattern string, h http.Handler) { s.router.Mount(pattern, h) }

// AddReadyCheck registers a readiness probe used by /readyz.
func (s *Server) AddReadyCheck(name string, c ReadyCheck) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checks[name] = c
}

func (s *Server) routes() chi.Router {
	r := chi.NewRouter()
	r.Use(requestID, observe, recoverer(s.log), accessLog(s.log), securityHeaders(s.cfg.Server.ContentBaseURL))
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/readyz", s.readyz)
	if s.cfg.Telemetry.Metrics {
		r.Handle("/metrics", metricsAuth(s.cfg.Telemetry.MetricsToken, telemetry.MetricsHandler()))
	}
	r.Get("/version", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, version.Get())
	})
	s.api = chi.NewRouter()
	s.api.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]any{"type": "about:blank", "title": "Not found", "status": 404, "code": "not_found"})
	})
	r.Mount("/api/v1", s.api)
	r.NotFound(web.Handler(s.webFS).ServeHTTP)
	return r
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	s.mu.RLock()
	defer s.mu.RUnlock()
	failed := map[string]string{}
	for name, c := range s.checks {
		if err := c(ctx); err != nil {
			failed[name] = err.Error()
		}
	}
	if len(failed) > 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unavailable", "failed": failed})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// metricsAuth requires the bearer token when one is configured.
func metricsAuth(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// SetContentHandler serves requests for the content origin's host with h;
// that host reaches nothing else.
func (s *Server) SetContentHandler(h http.Handler) {
	u, err := url.Parse(s.cfg.Server.ContentBaseURL)
	if err != nil || u.Host == "" {
		return
	}
	host, app := u.Host, s.router
	h = requestID(recoverer(s.log)(accessLog(s.log)(h)))
	s.content = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Host, host) {
			h.ServeHTTP(w, r)
			return
		}
		app.ServeHTTP(w, r)
	})
}

// Handler returns the root handler.
func (s *Server) Handler() http.Handler {
	if s.content != nil {
		return s.content
	}
	return s.router
}

// Run listens until ctx is cancelled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Server.Listen)
	if err != nil {
		return err
	}
	return s.Serve(ctx, ln)
}

// Serve is Run with a caller-provided listener.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.router,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	s.log.Info("listening", "addr", ln.Addr().String(), "base_url", s.cfg.Server.BaseURL, "version", version.Version)
	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s.log.Info("shutting down")
	return srv.Shutdown(shutdownCtx)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
