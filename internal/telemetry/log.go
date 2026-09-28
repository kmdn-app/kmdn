// Package telemetry sets up structured logging, Prometheus metrics and
// OpenTelemetry traces (docs/specs/13-operations.md#observability).
package telemetry

import (
	"context"
	"io"
	"log/slog"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
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
		return slog.New(ContextHandler(slog.NewTextHandler(w, opts)))
	}
	return slog.New(ContextHandler(slog.NewJSONHandler(w, opts)))
}

// ContextHandler adds org_id to every record logged with a context that
// carries an org (WithOrg), so a support search by org finds its requests,
// jobs and errors. Records that set org_id themselves keep theirs.
func ContextHandler(h slog.Handler) slog.Handler {
	if _, ok := h.(contextHandler); ok {
		return h
	}
	return contextHandler{h}
}

type contextHandler struct{ slog.Handler }

func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if org := OrgID(ctx); org != "" {
		has := false
		r.Attrs(func(a slog.Attr) bool {
			has = a.Key == "org_id"
			return !has
		})
		if !has {
			r.AddAttrs(slog.String("org_id", org))
		}
	}
	return h.Handler.Handle(ctx, r)
}

func (h contextHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return contextHandler{h.Handler.WithAttrs(as)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{h.Handler.WithGroup(name)}
}

// RequestInfo collects who a request is for, for the access log. The
// outermost middleware creates it; auth and org resolution fill it in.
type RequestInfo struct{ UserID, OrgID string }

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

type orgKey struct{}

// WithOrg says ctx works for orgID: its log records (ContextHandler), the
// request's access log line and the current span carry it. It's never a
// metric label: orgs are unbounded.
func WithOrg(ctx context.Context, orgID string) context.Context {
	if orgID == "" {
		return ctx
	}
	if ri, ok := ctx.Value(infoKey{}).(*RequestInfo); ok {
		ri.OrgID = orgID
	}
	trace.SpanFromContext(ctx).SetAttributes(attribute.String("kmdn.org_id", orgID))
	return context.WithValue(ctx, orgKey{}, orgID)
}

// OrgID is the org ctx works for, if any.
func OrgID(ctx context.Context) string {
	s, _ := ctx.Value(orgKey{}).(string)
	return s
}
