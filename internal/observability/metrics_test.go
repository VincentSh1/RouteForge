package observability

import (
	"context"
	"testing"
	"time"

	"github.com/VincentSh1/RouteForge/internal/openai"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestMetricsRecordBoundedLifecycleMeasurements(t *testing.T) {
	metrics, reader := testMetrics(t)
	ctx := context.Background()
	metrics.RecordRequest(ctx, "latency", true, "success", 3*time.Second)
	metrics.RecordRoutingSelection(ctx, "openai", "latency", true)
	metrics.RecordProviderAttempt(ctx, "openai", "timeout", true, false, 2*time.Second)
	metrics.RecordProviderAttempt(ctx, "anthropic", "success", true, true, 4*time.Second)
	metrics.RecordTTFC(ctx, "anthropic", 250*time.Millisecond)
	metrics.RecordFallback(ctx, "openai", "anthropic", "timeout")
	metrics.RecordCircuitTransition("openai", "closed", "open")
	metrics.RecordUsage(ctx, "anthropic", openai.NewUsage(10, 4, 14))
	metrics.RecordEstimatedCost(ctx, "anthropic", 17)

	collected := collectMetrics(t, reader)
	assertIntSum(t, collected, "routeforge_requests", 1)
	assertIntSum(t, collected, "routeforge_routing_selections", 1)
	assertIntSum(t, collected, "routeforge_provider_attempts", 2)
	assertIntSum(t, collected, "routeforge_fallbacks", 1)
	assertIntSum(t, collected, "routeforge_circuit_transitions", 1)
	assertIntSum(t, collected, "routeforge_tokens", 14)
	assertIntSum(t, collected, "routeforge_estimated_cost_micro_usd", 17)
	assertHistogramCount(t, collected, "routeforge_request_duration", 1, durationBuckets)
	assertHistogramCount(t, collected, "routeforge_provider_duration", 2, durationBuckets)
	assertHistogramCount(t, collected, "routeforge_provider_ttfc", 1, ttfcBuckets)

	allowedAttributes := map[string]bool{
		"routing_policy": true, "streaming": true, "outcome": true,
		"provider": true, "fallback": true, "from_provider": true,
		"to_provider": true, "reason": true, "from_state": true,
		"to_state": true, "direction": true,
	}
	for _, item := range collected {
		for _, set := range metricAttributeSets(item.Data) {
			for _, value := range set.ToSlice() {
				if !allowedAttributes[string(value.Key)] {
					t.Fatalf("metric %s contains unexpected attribute %q", item.Name, value.Key)
				}
			}
		}
	}
}

func TestMissingUsageAndCostCreateNoMeasurements(t *testing.T) {
	metrics, reader := testMetrics(t)
	metrics.RecordUsage(context.Background(), "openai", nil)
	collected := collectMetrics(t, reader)
	if _, ok := collected["routeforge_tokens"]; ok {
		t.Fatal("missing usage created a token measurement")
	}
	if _, ok := collected["routeforge_estimated_cost_micro_usd"]; ok {
		t.Fatal("missing cost created a cost measurement")
	}
}

func testMetrics(t *testing.T) (*Metrics, *sdkmetric.ManualReader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	metrics, err := NewMetrics(provider.Meter("routeforge-test"))
	if err != nil {
		t.Fatalf("NewMetrics() error = %v", err)
	}
	return metrics, reader
}

func collectMetrics(t *testing.T, reader *sdkmetric.ManualReader) map[string]metricdata.Metrics {
	t.Helper()
	var resourceMetrics metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &resourceMetrics); err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	collected := make(map[string]metricdata.Metrics)
	for _, scope := range resourceMetrics.ScopeMetrics {
		for _, item := range scope.Metrics {
			collected[item.Name] = item
		}
	}
	return collected
}

func assertIntSum(t *testing.T, collected map[string]metricdata.Metrics, name string, want int64) {
	t.Helper()
	sum, ok := collected[name].Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("metric %s is %T", name, collected[name].Data)
	}
	var got int64
	for _, point := range sum.DataPoints {
		got += point.Value
	}
	if got != want {
		t.Fatalf("metric %s sum = %d, want %d", name, got, want)
	}
}

func assertHistogramCount(t *testing.T, collected map[string]metricdata.Metrics, name string, want uint64, wantBounds []float64) {
	t.Helper()
	histogram, ok := collected[name].Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("metric %s is %T", name, collected[name].Data)
	}
	var count uint64
	for _, point := range histogram.DataPoints {
		count += point.Count
		if !equalFloatSlices(point.Bounds, wantBounds) {
			t.Fatalf("metric %s bounds = %v, want %v", name, point.Bounds, wantBounds)
		}
	}
	if count != want {
		t.Fatalf("metric %s count = %d, want %d", name, count, want)
	}
}

func metricAttributeSets(data metricdata.Aggregation) []attribute.Set {
	var sets []attribute.Set
	switch value := data.(type) {
	case metricdata.Sum[int64]:
		for _, point := range value.DataPoints {
			sets = append(sets, point.Attributes)
		}
	case metricdata.Histogram[float64]:
		for _, point := range value.DataPoints {
			sets = append(sets, point.Attributes)
		}
	}
	return sets
}

func equalFloatSlices(left, right []float64) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
