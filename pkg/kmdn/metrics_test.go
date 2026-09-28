package kmdn_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kmdn-app/kmdn/internal/telemetry"
	"github.com/kmdn-app/kmdn/pkg/kmdn"
)

func TestRegisterMetrics(t *testing.T) {
	c := prometheus.NewCounter(prometheus.CounterOpts{Name: "embedder_things_total", Help: "Things."})
	if err := kmdn.RegisterMetrics(c); err != nil {
		t.Fatal(err)
	}
	if err := kmdn.RegisterMetrics(c); err == nil {
		t.Fatal("registered twice")
	}
	c.Inc()
	rec := httptest.NewRecorder()
	telemetry.MetricsHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "embedder_things_total 1") {
		t.Fatal("not on /metrics")
	}
}
