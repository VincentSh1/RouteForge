package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/VincentSh1/RouteForge/internal/gateway"
	"github.com/VincentSh1/RouteForge/internal/observability"
	"github.com/VincentSh1/RouteForge/internal/provider/mock"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestRequestMetricsCoverSynchronousStreamingAndClientErrors(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	metrics, err := observability.NewMetrics(provider.Meter("httpapi-test"))
	if err != nil {
		t.Fatalf("NewMetrics() error = %v", err)
	}
	tracer := noop.NewTracerProvider().Tracer("httpapi-test")
	handler := TraceRequests(testHandler(&mock.Provider{}), tracer, propagation.TraceContext{}, gateway.RoutingPolicyDeterministic, metrics)

	for _, body := range []string{
		`{"model":"mock-model","messages":[{"role":"user","content":"private prompt"}]}`,
		`{"model":"mock-model","messages":[{"role":"user","content":"private prompt"}],"stream":true}`,
		`{"model":`,
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, chatRequest(http.MethodPost, body))
	}

	var result metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &result); err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	var requests metricdata.Sum[int64]
	var durations metricdata.Histogram[float64]
	for _, scope := range result.ScopeMetrics {
		for _, item := range scope.Metrics {
			switch item.Name {
			case "routeforge_requests":
				requests = item.Data.(metricdata.Sum[int64])
			case "routeforge_request_duration":
				durations = item.Data.(metricdata.Histogram[float64])
			}
		}
	}
	var requestCount int64
	seenStreaming := map[bool]bool{}
	seenOutcome := map[string]bool{}
	for _, point := range requests.DataPoints {
		requestCount += point.Value
		if value, ok := point.Attributes.Value("streaming"); ok {
			seenStreaming[value.AsBool()] = true
		}
		if value, ok := point.Attributes.Value("outcome"); ok {
			seenOutcome[value.AsString()] = true
		}
		for _, item := range point.Attributes.ToSlice() {
			if item.Key == "model" || item.Key == "request_id" || item.Value.AsString() == "private prompt" {
				t.Fatalf("unsafe request metric attribute: %v", item)
			}
		}
	}
	if requestCount != 3 || !seenStreaming[false] || !seenStreaming[true] || !seenOutcome["success"] || !seenOutcome["client_error"] {
		t.Fatalf("requests=%d streaming=%v outcomes=%v", requestCount, seenStreaming, seenOutcome)
	}
	var durationCount uint64
	for _, point := range durations.DataPoints {
		durationCount += point.Count
	}
	if durationCount != 3 {
		t.Fatalf("duration count = %d, want 3", durationCount)
	}
}
