package app

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/kmdn-app/kmdn/internal/config"
	"github.com/kmdn-app/kmdn/internal/users"
)

func TestMetricsAndTraces(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	repoID := connectLocal(t, a, admin, map[string]string{"docs/index.md": "# Handbook\n"})
	admin.do("GET", "/repos/"+repoID+"/tree", nil)

	res, err := http.Get(admin.base + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	m := string(b)
	for _, want := range []string{
		`kmdn_http_requests_total{code="200",method="GET",route="/api/v1/repos/{repo}/tree"}`,
		`kmdn_job_runs_total{kind="repo.sync",outcome="done"}`,
		`kmdn_git_command_duration_seconds_count{command="fetch",outcome="ok"}`,
		`kmdn_engine_runtimes `,
		`kmdn_websocket_connections 0`,
		`go_goroutines`,
	} {
		if !strings.Contains(m, want) {
			t.Errorf("metrics lack %s", want)
		}
	}

	names := map[string]bool{}
	for _, s := range rec.Ended() {
		names[s.Name()] = true
	}
	for _, want := range []string{"GET /api/v1/repos/{repo}/tree", "job repo.sync", "git fetch"} {
		if !names[want] {
			t.Errorf("no %q span in %v", want, names)
		}
	}

	// A token can guard them.
	_, guarded := newApp(t, func(c *config.Config) { c.Telemetry.MetricsToken = "scrape-me" })
	if res, _ := http.Get(guarded.base + "/metrics"); res.StatusCode != 401 {
		t.Fatalf("metrics without the token: %d", res.StatusCode)
	}
	req, _ := http.NewRequest("GET", guarded.base+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer scrape-me")
	if res, _ := http.DefaultClient.Do(req); res.StatusCode != 200 {
		t.Fatalf("metrics with the token: %d", res.StatusCode)
	}

	// Metrics can be turned off.
	_, off := newApp(t, func(c *config.Config) { c.Telemetry.Metrics = false })
	res, err = http.Get(off.base + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if strings.Contains(string(b), "kmdn_http_requests_total") {
		t.Fatal("metrics served while off")
	}
}
