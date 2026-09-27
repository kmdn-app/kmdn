package telemetry

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry holds kmdn's metrics (docs/specs/13-operations.md#observability).
// Metrics are process-wide: several apps in one process (tests) share them.
var Registry = prometheus.NewRegistry()

var (
	latency = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30}
	slow    = []float64{.1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120, 300}

	HTTPRequests = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "kmdn", Name: "http_requests_total", Help: "HTTP requests by route and status."}, []string{"method", "route", "code"})
	HTTPDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: "kmdn", Name: "http_request_duration_seconds", Help: "HTTP request latency (WebSockets excluded).", Buckets: latency}, []string{"method", "route"})

	YjsUpdates = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "kmdn", Name: "yjs_updates_total", Help: "Collaborative document updates received."})

	EngineCalls = prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: "kmdn", Name: "engine_call_duration_seconds", Help: "Doc engine (JS runtime) call latency.", Buckets: latency}, []string{"outcome"})

	JobRuns     = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "kmdn", Name: "job_runs_total", Help: "Background job runs by outcome (done, retry, failed)."}, []string{"kind", "outcome"})
	JobDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: "kmdn", Name: "job_duration_seconds", Help: "Background job run time.", Buckets: slow}, []string{"kind"})
	JobDelay    = prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: "kmdn", Name: "job_delay_seconds", Help: "Time from a job being due to it starting.", Buckets: slow}, []string{"kind"})

	GitCommands = prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: "kmdn", Name: "git_command_duration_seconds", Help: "git commands (fetch is the mirror fetch).", Buckets: slow}, []string{"command", "outcome"})
	ForgeCalls  = prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: "kmdn", Name: "forge_request_duration_seconds", Help: "Forge API requests (GitHub, GitLab).", Buckets: latency}, []string{"host", "code"})

	Publishes = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "kmdn", Name: "publish_total", Help: "Publish outcomes (pushed, pull_request, failed)."}, []string{"outcome"})

	LLMTokens   = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "kmdn", Name: "llm_tokens_total", Help: "Model tokens by provider, model and type (input, output, cache_read, cache_write)."}, []string{"provider", "model", "type"})
	LLMDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: "kmdn", Name: "llm_call_duration_seconds", Help: "Model call latency.", Buckets: slow}, []string{"provider", "model", "outcome"})

	MCPCalls = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "kmdn", Name: "mcp_calls_total", Help: "MCP tool calls and resource reads by tool and agent key id."}, []string{"tool", "key"})
)

// Sources report live state at scrape time. The app sets them.
type Sources struct {
	WebSockets func() int
	Rooms      func() int
	// Engine returns runtimes in use and the pool size.
	Engine func() (inUse, size int)
	// Jobs returns queued (pending) and failed jobs by kind.
	Jobs func(ctx context.Context) (queued, failed map[string]int, err error)
}

var sources atomic.Pointer[Sources]

// SetSources replaces the live-state sources.
func SetSources(s Sources) { sources.Store(&s) }

type gauges struct {
	ws, rooms, engineInUse, engineSize, queued, failed *prometheus.Desc
}

func (g gauges) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{g.ws, g.rooms, g.engineInUse, g.engineSize, g.queued, g.failed} {
		ch <- d
	}
}

func (g gauges) Collect(ch chan<- prometheus.Metric) {
	s := sources.Load()
	if s == nil {
		return
	}
	if s.WebSockets != nil {
		ch <- prometheus.MustNewConstMetric(g.ws, prometheus.GaugeValue, float64(s.WebSockets()))
	}
	if s.Rooms != nil {
		ch <- prometheus.MustNewConstMetric(g.rooms, prometheus.GaugeValue, float64(s.Rooms()))
	}
	if s.Engine != nil {
		in, size := s.Engine()
		ch <- prometheus.MustNewConstMetric(g.engineInUse, prometheus.GaugeValue, float64(in))
		ch <- prometheus.MustNewConstMetric(g.engineSize, prometheus.GaugeValue, float64(size))
	}
	if s.Jobs != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if queued, failed, err := s.Jobs(ctx); err == nil {
			for k, n := range queued {
				ch <- prometheus.MustNewConstMetric(g.queued, prometheus.GaugeValue, float64(n), k)
			}
			for k, n := range failed {
				ch <- prometheus.MustNewConstMetric(g.failed, prometheus.GaugeValue, float64(n), k)
			}
		}
	}
}

func init() {
	Registry.MustRegister(
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		HTTPRequests, HTTPDuration, YjsUpdates, EngineCalls, JobRuns, JobDuration, JobDelay, GitCommands, ForgeCalls, Publishes, LLMTokens, LLMDuration, MCPCalls,
		gauges{
			ws:          prometheus.NewDesc("kmdn_websocket_connections", "Open WebSocket connections.", nil, nil),
			rooms:       prometheus.NewDesc("kmdn_active_rooms", "Collaborative documents open in memory.", nil, nil),
			engineInUse: prometheus.NewDesc("kmdn_engine_runtimes_in_use", "Doc engine runtimes busy.", nil, nil),
			engineSize:  prometheus.NewDesc("kmdn_engine_runtimes", "Doc engine pool size.", nil, nil),
			queued:      prometheus.NewDesc("kmdn_jobs_queued", "Background jobs waiting to run.", []string{"kind"}, nil),
			failed:      prometheus.NewDesc("kmdn_jobs_failed", "Background jobs that failed for good (retry from the admin console).", []string{"kind"}, nil),
		},
	)
}

// MetricsHandler serves /metrics.
func MetricsHandler() http.Handler {
	return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{Registry: Registry})
}

// Outcome labels an error.
func Outcome(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}
