package telemetry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel"
)

func TestTracingExportsOverOTLP(t *testing.T) {
	var got atomic.Int32
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/v1/traces" && strings.Contains(string(b), "forge-call") {
			got.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	prev := otel.GetTracerProvider()
	defer otel.SetTracerProvider(prev)
	shutdown, err := SetupTracing(context.Background(), collector.URL, "test")
	if err != nil {
		t.Fatal(err)
	}
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Traceparent") != "" {
			t.Error("trace context sent to a forge")
		}
		w.WriteHeader(http.StatusTeapot)
	}))
	defer forge.Close()
	ctx, span := Tracer().Start(context.Background(), "forge-call")
	req, _ := http.NewRequestWithContext(ctx, "GET", forge.URL+"/repos", nil)
	res, err := (&http.Client{Transport: Transport(nil)}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	span.End()
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got.Load() == 0 {
		t.Fatal("no spans reached the collector")
	}
	host := strings.TrimPrefix(forge.URL, "http://")
	if n := testutil.CollectAndCount(ForgeCalls); n == 0 {
		t.Fatal("forge call not measured")
	}
	if !strings.Contains(gather(t), `kmdn_forge_request_duration_seconds_count{code="418",host="`+host+`"} 1`) {
		t.Fatalf("forge metric: %s", gather(t))
	}
}

func gather(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	return rec.Body.String()
}
