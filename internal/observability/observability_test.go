package observability

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDisabledSetupUsesNoopTracing(t *testing.T) {
	setup, err := New(context.Background(), Config{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, span := setup.Tracer().Start(context.Background(), "test")
	if span.SpanContext().IsValid() {
		t.Fatal("disabled tracing produced a recording span")
	}
	span.End()
	if err := setup.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if got := setup.Propagator().Fields(); len(got) != 2 || got[0] != "traceparent" || got[1] != "tracestate" {
		t.Fatalf("propagator fields = %v", got)
	}
	if setup.MetricsHandler() != nil {
		t.Fatal("disabled metrics created a Prometheus handler")
	}
	setup.Metrics().RecordRequest(context.Background(), "deterministic", false, "success", time.Second)
}

func TestTracingAndMetricsAreIndependentlyConfigurable(t *testing.T) {
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()

	for _, test := range []struct {
		name           string
		tracing        bool
		metrics        bool
		wantValidSpan  bool
		wantPrometheus bool
	}{
		{name: "both disabled"},
		{name: "tracing only", tracing: true, wantValidSpan: true},
		{name: "metrics only", metrics: true, wantPrometheus: true},
		{name: "both enabled", tracing: true, metrics: true, wantValidSpan: true, wantPrometheus: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			setup, err := New(context.Background(), Config{
				TracingEnabled: test.tracing, TraceEndpoint: collector.URL, MetricsEnabled: test.metrics,
			})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			_, span := setup.Tracer().Start(context.Background(), "test")
			if span.SpanContext().IsValid() != test.wantValidSpan {
				t.Fatalf("valid span = %v, want %v", span.SpanContext().IsValid(), test.wantValidSpan)
			}
			span.End()
			if (setup.MetricsHandler() != nil) != test.wantPrometheus {
				t.Fatalf("Prometheus handler present = %v, want %v", setup.MetricsHandler() != nil, test.wantPrometheus)
			}
			shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := setup.Shutdown(shutdownCtx); err != nil {
				t.Fatalf("Shutdown() error = %v", err)
			}
		})
	}
}

func TestMetricsSetupUsesCustomPrometheusRegistry(t *testing.T) {
	setup, err := New(context.Background(), Config{MetricsEnabled: true})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = setup.Shutdown(context.Background()) })
	setup.Metrics().RecordRequest(context.Background(), "deterministic", false, "success", 250*time.Millisecond)

	request := httptest.NewRequest("GET", "/metrics", nil)
	response := httptest.NewRecorder()
	setup.MetricsHandler().ServeHTTP(response, request)
	body, err := io.ReadAll(response.Result().Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	output := string(body)
	for _, want := range []string{
		"routeforge_requests_total",
		"routeforge_request_duration_seconds",
		`outcome="success"`,
		`routing_policy="deterministic"`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("scrape is missing %q:\n%s", want, output)
		}
	}
	for _, prohibited := range []string{"go_gc_", "process_", "target_info", "otel_scope_info", "private prompt", "Authorization"} {
		if strings.Contains(output, prohibited) {
			t.Fatalf("scrape contains prohibited text %q", prohibited)
		}
	}
}

func TestTraceEndpointAddsDefaultTracePath(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "https://collector.example", want: "https://collector.example/v1/traces"},
		{input: "http://127.0.0.1:4318/", want: "http://127.0.0.1:4318/v1/traces"},
		{input: "https://collector.example/custom", want: "https://collector.example/custom"},
	} {
		if got := traceEndpoint(test.input); got != test.want {
			t.Fatalf("traceEndpoint(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}
