package telemetry

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// Work for an org logs its org_id and tags its span with it; records that
// name an org keep theirs, and work for no org adds nothing.
func TestWithOrg(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, "json", "info").With("service", "kmdn")
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	ctx, span := tp.Tracer("t").Start(context.Background(), "request")
	ctx, info := WithRequestInfo(ctx)
	ctx = WithOrg(ctx, "org_1")
	log.InfoContext(ctx, "one")
	log.InfoContext(ctx, "two", "org_id", "org_2")
	log.InfoContext(context.Background(), "three")
	span.End()

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if !strings.Contains(lines[0], `"org_id":"org_1"`) || strings.Count(lines[1], "org_id") != 1 || !strings.Contains(lines[1], "org_2") || strings.Contains(lines[2], "org_id") {
		t.Fatalf("logs:\n%s", buf.String())
	}
	if info.OrgID != "org_1" || OrgID(ctx) != "org_1" {
		t.Fatalf("request info: %+v", info)
	}
	found := false
	for _, a := range rec.Ended()[0].Attributes() {
		found = found || (a.Key == "kmdn.org_id" && a.Value.AsString() == "org_1")
	}
	if !found {
		t.Fatal("span without kmdn.org_id")
	}
	if ContextHandler(ContextHandler(slog.NewTextHandler(&buf, nil))) == nil {
		t.Fatal("double wrap")
	}
}
