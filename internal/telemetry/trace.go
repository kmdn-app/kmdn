package telemetry

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Tracer creates kmdn's spans. Until SetupTracing runs it is a no-op.
func Tracer() trace.Tracer { return otel.Tracer("github.com/kmdn-app/kmdn") }

// SetupTracing exports spans over OTLP/HTTP to endpoint: a collector's base
// URL (http://otel-collector:4318, /v1/traces is added) or a full traces
// URL. It returns a shutdown function that flushes.
func SetupTracing(ctx context.Context, endpoint, version string) (func(context.Context) error, error) {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%q isn't an http(s) URL", endpoint)
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/v1/traces"
	}
	exp, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(u.String()))
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", "kmdn"), attribute.String("service.version", version))),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	return tp.Shutdown, nil
}

// End finishes a span, recording err.
func End(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

// Transport instruments outgoing forge API calls: a client span and a
// latency metric per host. Trace context isn't sent to third parties.
func Transport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return roundTripper{base}
}

type roundTripper struct{ base http.RoundTripper }

func (t roundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	ctx, span := Tracer().Start(r.Context(), "HTTP "+r.Method+" "+r.URL.Host, trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("http.request.method", r.Method), attribute.String("server.address", r.URL.Host), attribute.String("url.path", r.URL.Path)))
	start := time.Now()
	res, err := t.base.RoundTrip(r.WithContext(ctx))
	code := "error"
	if err == nil {
		code = strconv.Itoa(res.StatusCode)
		span.SetAttributes(attribute.Int("http.response.status_code", res.StatusCode))
		if res.StatusCode >= 500 {
			span.SetStatus(codes.Error, res.Status)
		}
	}
	ForgeCalls.WithLabelValues(r.URL.Host, code).Observe(time.Since(start).Seconds())
	End(span, err)
	return res, err
}
