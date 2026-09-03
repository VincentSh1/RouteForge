package gateway

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/VincentSh1/RouteForge/internal/accounting"
	"github.com/VincentSh1/RouteForge/internal/observability"
	"github.com/VincentSh1/RouteForge/internal/openai"
	"github.com/VincentSh1/RouteForge/internal/provider"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestProviderMetricsRecordAttemptsFallbackUsageAndCost(t *testing.T) {
	first := &usageCompletionProvider{
		name: "first", usage: openai.NewUsage(5, 2, 7),
		err: provider.NewError(provider.ErrorTimeout, "first", errors.New("private detail")),
	}
	second := &usageCompletionProvider{name: "second", usage: openai.NewUsage(5, 4, 9)}
	service := NewAuto(testResolver(), first, second)
	inputRate, outputRate := uint64(1_000_000), uint64(2_000_000)
	service.SetPricing(accounting.PriceBook{
		{Provider: "first", Model: "first-model"}:   {InputMicroUSDPerMillion: &inputRate, OutputMicroUSDPerMillion: &outputRate},
		{Provider: "second", Model: "second-model"}: {InputMicroUSDPerMillion: &inputRate, OutputMicroUSDPerMillion: &outputRate},
	})
	metrics, reader := gatewayTestMetrics(t)
	service.SetMetrics(metrics)

	if _, err := service.Complete(context.Background(), validRequest()); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	collected := collectGatewayMetrics(t, reader)
	assertGatewayIntSum(t, collected, "routeforge_routing_selections", 1)
	assertGatewayIntSum(t, collected, "routeforge_provider_attempts", 2)
	assertGatewayIntSum(t, collected, "routeforge_fallbacks", 1)
	assertGatewayIntSum(t, collected, "routeforge_tokens", 16)
	assertGatewayIntSum(t, collected, "routeforge_estimated_cost_micro_usd", 22)
	assertGatewayHistogramCount(t, collected, "routeforge_provider_duration", 2)
	assertGatewayAttribute(t, collected["routeforge_fallbacks"].Data, "reason", "timeout")
	assertGatewayAttribute(t, collected["routeforge_routing_selections"].Data, "provider", "first")
}

func TestProviderMetricsReuseProviderOutcomeClassification(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		cancel  bool
		outcome string
	}{
		{name: "unavailable", err: provider.NewError(provider.ErrorUnavailable, "recording", errors.New("detail")), outcome: "unavailable"},
		{name: "rate limited", err: provider.NewError(provider.ErrorRateLimited, "recording", errors.New("detail")), outcome: "rate_limited"},
		{name: "invalid request", err: provider.NewError(provider.ErrorInvalidRequest, "recording", errors.New("detail")), outcome: "invalid_request"},
		{name: "cancellation", err: context.Canceled, cancel: true, outcome: "cancellation"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := New(&recordingProvider{err: test.err}, testResolver())
			metrics, reader := gatewayTestMetrics(t)
			service.SetMetrics(metrics)
			ctx := context.Background()
			if test.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			_, _ = service.Complete(ctx, validRequest())
			collected := collectGatewayMetrics(t, reader)
			assertGatewayAttribute(t, collected["routeforge_provider_attempts"].Data, "outcome", test.outcome)
		})
	}
}

func TestFallbackMetricRequiresASecondActualAttempt(t *testing.T) {
	first := &recordingProvider{name: "first", err: provider.NewError(provider.ErrorTimeout, "first", errors.New("detail"))}
	second := &recordingProvider{name: "second"}
	service := NewAuto(testResolver(), first, second)
	secondHealth := service.health.providers["second"]
	secondHealth.mu.Lock()
	secondHealth.state = circuitOpen
	secondHealth.openUntil = time.Now().Add(time.Minute)
	secondHealth.mu.Unlock()
	metrics, reader := gatewayTestMetrics(t)
	service.SetMetrics(metrics)

	_, _ = service.Complete(context.Background(), validRequest())
	collected := collectGatewayMetrics(t, reader)
	assertGatewayIntSum(t, collected, "routeforge_provider_attempts", 1)
	if _, ok := collected["routeforge_fallbacks"]; ok {
		t.Fatal("a skipped second provider produced a fallback metric")
	}
}

func TestStreamingMetricsRecordTTFCOnceAndProviderLifetime(t *testing.T) {
	clock := newManualClock()
	upstream := &timedStreamingProvider{name: "first", clock: clock, steps: []streamTelemetryStep{
		{advance: time.Millisecond, chunk: provider.StreamChunk{Usage: openai.NewUsage(3, 2, 5)}},
		{advance: time.Millisecond, chunk: provider.StreamChunk{Role: "assistant"}},
		{advance: 3 * time.Millisecond, chunk: provider.StreamChunk{Content: "hello"}},
		{advance: 2 * time.Millisecond, chunk: provider.StreamChunk{Content: " again"}},
		{advance: time.Millisecond, chunk: provider.StreamChunk{FinishReason: "stop"}},
		{advance: time.Millisecond, err: io.EOF},
	}}
	service := New(upstream, testResolver())
	service.telemetry = newTelemetryTracker([]string{"first"}, 4, clock.Now)
	metrics, reader := gatewayTestMetrics(t)
	service.SetMetrics(metrics)

	if err := service.Stream(context.Background(), streamRequest(), func(provider.StreamChunk) error { return nil }); err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	collected := collectGatewayMetrics(t, reader)
	assertGatewayHistogramCount(t, collected, "routeforge_provider_ttfc", 1)
	assertGatewayHistogramCount(t, collected, "routeforge_provider_duration", 1)
	assertGatewayIntSum(t, collected, "routeforge_provider_attempts", 1)
	assertGatewayIntSum(t, collected, "routeforge_tokens", 5)
	if got := gatewayHistogramSum(t, collected, "routeforge_provider_ttfc"); got != 0.005 {
		t.Fatalf("TTFC sum = %v, want 0.005", got)
	}
	if got := gatewayHistogramSum(t, collected, "routeforge_provider_duration"); got != 0.009 {
		t.Fatalf("provider duration sum = %v, want 0.009", got)
	}
}

func TestPostCommitStreamFailureDoesNotRecordFallback(t *testing.T) {
	first := &streamingTestProvider{
		name: "first", chunks: []provider.StreamChunk{{Content: "partial"}}, errAfter: 1,
		err: provider.NewError(provider.ErrorUnavailable, "first", errors.New("private detail")),
	}
	second := &streamingTestProvider{name: "second", chunks: []provider.StreamChunk{{Content: "must not run"}}}
	service := NewAuto(testResolver(), first, second)
	metrics, reader := gatewayTestMetrics(t)
	service.SetMetrics(metrics)
	if err := service.Stream(context.Background(), streamRequest(), func(provider.StreamChunk) error { return nil }); err == nil {
		t.Fatal("Stream() error = nil")
	}
	collected := collectGatewayMetrics(t, reader)
	assertGatewayIntSum(t, collected, "routeforge_provider_attempts", 1)
	if _, ok := collected["routeforge_fallbacks"]; ok {
		t.Fatal("post-commit failure recorded a fallback")
	}
}

func TestCircuitTransitionMetricsFollowAuthoritativeTransitions(t *testing.T) {
	clock := newManualClock()
	upstream := &timedCompletionProvider{
		name: "first", clock: clock,
		err: provider.NewError(provider.ErrorUnavailable, "first", errors.New("private detail")),
	}
	service, err := NewAutoWithRoutingClock(testResolver(), CircuitConfig{FailureThreshold: 1, OpenDuration: time.Minute}, RoutingConfig{}, clock, upstream)
	if err != nil {
		t.Fatalf("NewAutoWithRoutingClock() error = %v", err)
	}
	metrics, reader := gatewayTestMetrics(t)
	service.SetMetrics(metrics)
	_, _ = service.Complete(context.Background(), validRequest())
	if service.ProviderEligible("first") {
		t.Fatal("open circuit reported eligible before cooldown")
	}
	clock.Advance(time.Minute)
	if !service.ProviderEligible("first") || !service.ProviderEligible("first") {
		t.Fatal("expired open circuit reported ineligible")
	}
	upstream.err = nil
	if _, err := service.Complete(context.Background(), validRequest()); err != nil {
		t.Fatalf("half-open Complete() error = %v", err)
	}
	upstream.err = provider.NewError(provider.ErrorUnavailable, "first", errors.New("private detail"))
	_, _ = service.Complete(context.Background(), validRequest())
	clock.Advance(time.Minute)
	_, _ = service.Complete(context.Background(), validRequest())

	collected := collectGatewayMetrics(t, reader)
	assertGatewayIntSum(t, collected, "routeforge_circuit_transitions", 6)
	transitions := collected["routeforge_circuit_transitions"].Data
	for _, pair := range [][2]string{{"closed", "open"}, {"open", "half_open"}, {"half_open", "closed"}, {"half_open", "open"}} {
		if !gatewayMetricHasAttributes(transitions, map[string]string{"from_state": pair[0], "to_state": pair[1]}) {
			t.Fatalf("transition %s -> %s was not recorded", pair[0], pair[1])
		}
	}
}

func gatewayTestMetrics(t *testing.T) (*observability.Metrics, *sdkmetric.ManualReader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	metrics, err := observability.NewMetrics(provider.Meter("gateway-test"))
	if err != nil {
		t.Fatalf("NewMetrics() error = %v", err)
	}
	return metrics, reader
}

func collectGatewayMetrics(t *testing.T, reader *sdkmetric.ManualReader) map[string]metricdata.Metrics {
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

func assertGatewayIntSum(t *testing.T, collected map[string]metricdata.Metrics, name string, want int64) {
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

func assertGatewayHistogramCount(t *testing.T, collected map[string]metricdata.Metrics, name string, want uint64) {
	t.Helper()
	histogram, ok := collected[name].Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("metric %s is %T", name, collected[name].Data)
	}
	var got uint64
	for _, point := range histogram.DataPoints {
		got += point.Count
	}
	if got != want {
		t.Fatalf("metric %s count = %d, want %d", name, got, want)
	}
}

func gatewayHistogramSum(t *testing.T, collected map[string]metricdata.Metrics, name string) float64 {
	t.Helper()
	histogram, ok := collected[name].Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("metric %s is %T", name, collected[name].Data)
	}
	var sum float64
	for _, point := range histogram.DataPoints {
		sum += point.Sum
	}
	return sum
}

func assertGatewayAttribute(t *testing.T, data metricdata.Aggregation, key, want string) {
	t.Helper()
	sum, ok := data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("metric data is %T", data)
	}
	for _, point := range sum.DataPoints {
		if value, ok := point.Attributes.Value(attribute.Key(key)); ok && value.AsString() == want {
			return
		}
	}
	t.Fatalf("attribute %s=%q not found", key, want)
}

func gatewayMetricHasAttributes(data metricdata.Aggregation, wanted map[string]string) bool {
	sum, ok := data.(metricdata.Sum[int64])
	if !ok {
		return false
	}
	for _, point := range sum.DataPoints {
		matches := true
		for key, want := range wanted {
			value, ok := point.Attributes.Value(attribute.Key(key))
			if !ok || value.AsString() != want {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}
