// Package telemetry sets up structured logging, Prometheus metrics and
// OpenTelemetry traces (docs/specs/13-operations.md#observability).
package telemetry

import (
	"context"
	"io"
	"log/slog"
	"strings"
)

// NewLogger builds a slog.Logger. format is "json" or "text"; level is
// debug, info, warn or error (default info).
func NewLogger(w io.Writer, format, level string) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lv}
	if format == "text" {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}

// RequestInfo collects who a request is for, for the access log. The
// outermost middleware creates it; auth fills it in.
type RequestInfo struct{ UserID string }

type infoKey struct{}

// WithRequestInfo adds an empty RequestInfo to ctx.
func WithRequestInfo(ctx context.Context) (context.Context, *RequestInfo) {
	ri := &RequestInfo{}
	return context.WithValue(ctx, infoKey{}, ri), ri
}

// SetUser records the signed-in user for the access log.
func SetUser(ctx context.Context, id string) {
	if ri, ok := ctx.Value(infoKey{}).(*RequestInfo); ok {
		ri.UserID = id
	}
}
