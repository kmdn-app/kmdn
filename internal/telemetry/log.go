// Package telemetry sets up structured logging (and, later, metrics and traces).
package telemetry

import (
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
