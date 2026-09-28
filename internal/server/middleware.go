package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/kmdn-app/kmdn/internal/telemetry"
)

type ctxKey int

const requestIDKey ctxKey = iota

// RequestID returns the request id stored by the requestID middleware.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" || len(id) > 64 {
			var b [8]byte
			_, _ = rand.Read(b[:])
			id = hex.EncodeToString(b[:])
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer (needed for
// WebSocket hijacking and flushing).
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func accessLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			ctx, info := telemetry.WithRequestInfo(r.Context())
			next.ServeHTTP(rec, r.WithContext(ctx))
			if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" || r.URL.Path == "/metrics" {
				return
			}
			attrs := []slog.Attr{
				slog.String("request_id", RequestID(r.Context())),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Int("bytes", rec.bytes),
				slog.Duration("duration", time.Since(start)),
			}
			if info.UserID != "" {
				attrs = append(attrs, slog.String("user_id", info.UserID))
			}
			if info.OrgID != "" {
				attrs = append(attrs, slog.String("org_id", info.OrgID))
			}
			if rc := chi.RouteContext(r.Context()); rc != nil {
				for _, p := range []string{"repo", "revision"} {
					if v := rc.URLParam(p); v != "" {
						attrs = append(attrs, slog.String(p+"_id", v))
					}
				}
			}
			log.LogAttrs(r.Context(), slog.LevelInfo, "http", attrs...)
		})
	}
}

func recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
						panic(v)
					}
					log.Error("panic", "request_id", RequestID(r.Context()), "error", v, "stack", string(debug.Stack()))
					http.Error(w, "internal server error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// securityHeaders applies the baseline from docs/specs/09-auth-permissions.md.
// form-action allows https: because the GitHub App manifest flow POSTs a form
// to github.com (or a GitHub Enterprise Server host).
func securityHeaders(contentOrigin string) func(http.Handler) http.Handler {
	img := "'self' data: blob:"
	if contentOrigin != "" {
		img += " " + strings.TrimRight(contentOrigin, "/")
	}
	csp := "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
		"img-src " + img + "; font-src 'self' data:; connect-src 'self' ws: wss:; " +
		"worker-src 'self' blob:; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self' https:"
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Content-Security-Policy", csp)
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
			next.ServeHTTP(w, r)
		})
	}
}

// observe records a server span and request metrics, labelled by route
// pattern (known after routing) rather than path, to bound cardinality.
func observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := telemetry.Tracer().Start(ctx, r.Method, trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attribute.String("http.request.method", r.Method), attribute.String("url.path", r.URL.Path), attribute.String("kmdn.request_id", RequestID(r.Context()))))
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r.WithContext(ctx))
		route := "spa"
		if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" && rc.RoutePattern() != "/*" {
			route = rc.RoutePattern()
		}
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		telemetry.HTTPRequests.WithLabelValues(r.Method, route, strconv.Itoa(status)).Inc()
		if status != http.StatusSwitchingProtocols {
			telemetry.HTTPDuration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
		}
		span.SetName(r.Method + " " + route)
		span.SetAttributes(attribute.String("http.route", route), attribute.Int("http.response.status_code", status))
		if status >= 500 {
			span.SetStatus(codes.Error, http.StatusText(status))
		}
		span.End()
	})
}
