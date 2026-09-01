package gateway

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/VincentSh1/RouteForge/internal/accounting"
	"github.com/VincentSh1/RouteForge/internal/model"
	"github.com/VincentSh1/RouteForge/internal/openai"
	"github.com/VincentSh1/RouteForge/internal/provider"
)

func TestCostLatencyRoutingPolicyConfiguration(t *testing.T) {
	percent := uint64(20)
	policy, ranksEligible, err := newRoutingPolicy(RoutingConfig{
		Policy:                       RoutingPolicyCostLatency,
		MinSamples:                   5,
		SampleMaxAge:                 time.Minute,
		ExplorationInterval:          10,
		MaxLatencyOverFastestPercent: &percent,
	})
	if err != nil {
		t.Fatalf("newRoutingPolicy() error = %v", err)
	}
	if _, ok := policy.(*costLatencyRoutingPolicy); !ok || !ranksEligible {
		t.Fatalf("newRoutingPolicy() = %T, ranks eligible = %v", policy, ranksEligible)
	}

	if _, _, err := newRoutingPolicy(RoutingConfig{
		Policy: RoutingPolicyCostLatency, MinSamples: 5, SampleMaxAge: time.Minute, ExplorationInterval: 10,
	}); err == nil {
		t.Fatal("missing tolerance error = nil")
	}
}

func TestCostLatencyPartitionBoundaryAndConstraintPrecedence(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	providers := []provider.Provider{
		&recordingProvider{name: "first"},
		&recordingProvider{name: "second"},
		&recordingProvider{name: "third"},
	}
	policy := testCostLatencyPolicy(20, 3, time.Minute, 10)
	snapshots := map[string]ProviderTelemetrySnapshot{
		"first":  routingSnapshot(now, repeatedDuration(200*time.Millisecond, 3), nil, nil),
		"second": routingSnapshot(now, repeatedDuration(240*time.Millisecond, 3), nil, nil),
		"third":  routingSnapshot(now, repeatedDuration(241*time.Millisecond, 3), nil, nil),
	}
	prices := map[string]accounting.Rates{
		"first":  costRates(10, 10),
		"second": costRates(5, 5),
		"third":  costRates(1, 1),
	}

	assertProviderOrder(t, policy.order(providers, nonStreamingMode, snapshots, prices, now), "second", "first", "third")
}

func TestCostLatencyZeroToleranceAdmitsOnlyFastestTies(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	providers := []provider.Provider{
		&recordingProvider{name: "first"},
		&recordingProvider{name: "second"},
		&recordingProvider{name: "third"},
	}
	policy := testCostLatencyPolicy(0, 1, time.Minute, 10)
	snapshots := map[string]ProviderTelemetrySnapshot{
		"first":  routingSnapshot(now, []time.Duration{200 * time.Millisecond}, nil, nil),
		"second": routingSnapshot(now, []time.Duration{200 * time.Millisecond}, nil, nil),
		"third":  routingSnapshot(now, []time.Duration{201 * time.Millisecond}, nil, nil),
	}
	prices := map[string]accounting.Rates{
		"first":  costRates(10, 10),
		"second": costRates(1, 1),
		"third":  costRates(0, 0),
	}

	assertProviderOrder(t, policy.order(providers, nonStreamingMode, snapshots, prices, now), "second", "first", "third")
}

func TestCostLatencyOnlyFastestIsAcceptable(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	providers := []provider.Provider{
		&recordingProvider{name: "first"},
		&recordingProvider{name: "second"},
		&recordingProvider{name: "third"},
	}
	policy := testCostLatencyPolicy(20, 1, time.Minute, 10)
	snapshots := map[string]ProviderTelemetrySnapshot{
		"first":  routingSnapshot(now, []time.Duration{300 * time.Millisecond}, nil, nil),
		"second": routingSnapshot(now, []time.Duration{200 * time.Millisecond}, nil, nil),
		"third":  routingSnapshot(now, []time.Duration{250 * time.Millisecond}, nil, nil),
	}
	prices := map[string]accounting.Rates{
		"first":  costRates(1, 1),
		"second": costRates(100, 100),
		"third":  costRates(1, 1),
	}

	assertProviderOrder(t, policy.order(providers, nonStreamingMode, snapshots, prices, now), "second", "first", "third")
}

func TestCostLatencyEconomicOrderingInsideAcceptablePartition(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	providers := routingProviders()
	policy := testCostLatencyPolicy(20, 1, time.Minute, 10)
	snapshots := map[string]ProviderTelemetrySnapshot{
		"first":  routingSnapshot(now, []time.Duration{100 * time.Millisecond}, nil, nil),
		"second": routingSnapshot(now, []time.Duration{110 * time.Millisecond}, nil, nil),
	}

	tests := []struct {
		name      string
		second    accounting.Rates
		wantFirst string
	}{
		{name: "cheaper input and output", second: costRates(9, 9), wantFirst: "second"},
		{name: "equal input and cheaper output", second: costRates(10, 9), wantFirst: "second"},
		{name: "cheaper input and equal output", second: costRates(9, 10), wantFirst: "second"},
		{name: "identical prices", second: costRates(10, 10), wantFirst: "first"},
		{name: "conflicting prices", second: costRates(9, 11), wantFirst: "first"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prices := map[string]accounting.Rates{"first": costRates(10, 10), "second": test.second}
			ordered := policy.order(providers, nonStreamingMode, snapshots, prices, now)
			if ordered[0].Name() != test.wantFirst {
				t.Fatalf("first provider = %q, want %q", ordered[0].Name(), test.wantFirst)
			}
		})
	}

	partial := costRates(1, 1)
	partial.OutputMicroUSDPerMillion = nil
	for name, prices := range map[string]map[string]accounting.Rates{
		"missing pricing": {"first": costRates(10, 10)},
		"partial pricing": {"first": costRates(10, 10), "second": partial},
	} {
		t.Run(name, func(t *testing.T) {
			assertProviderOrder(t, policy.order(providers, nonStreamingMode, snapshots, prices, now), "first", "second")
		})
	}
}

func TestCostLatencyStableMultipleProviderOrdering(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	providers := []provider.Provider{
		&recordingProvider{name: "first"},
		&recordingProvider{name: "second"},
		&recordingProvider{name: "third"},
		&recordingProvider{name: "fourth"},
	}
	policy := testCostLatencyPolicy(20, 1, time.Minute, 10)
	snapshots := map[string]ProviderTelemetrySnapshot{
		"first":  routingSnapshot(now, []time.Duration{100 * time.Millisecond}, nil, nil),
		"second": routingSnapshot(now, []time.Duration{105 * time.Millisecond}, nil, nil),
		"third":  routingSnapshot(now, []time.Duration{110 * time.Millisecond}, nil, nil),
		"fourth": routingSnapshot(now, []time.Duration{121 * time.Millisecond}, nil, nil),
	}
	prices := map[string]accounting.Rates{
		"first":  costRates(10, 10),
		"second": costRates(8, 12),
		"third":  costRates(9, 9),
		"fourth": costRates(1, 1),
	}

	assertProviderOrder(t, policy.order(providers, nonStreamingMode, snapshots, prices, now), "second", "third", "first", "fourth")
}

func TestCostLatencyToleranceComparisonIsOverflowSafe(t *testing.T) {
	if !withinLatencyTolerance(time.Duration(math.MaxInt64), time.Duration(math.MaxInt64/2), math.MaxUint64) {
		t.Fatal("large tolerance rejected an in-range duration")
	}
	if withinLatencyTolerance(time.Duration(math.MaxInt64), time.Duration(math.MaxInt64-1), 0) {
		t.Fatal("zero tolerance accepted a slower duration")
	}
	if !withinLatencyTolerance(120*time.Millisecond, 100*time.Millisecond, 20) {
		t.Fatal("exact boundary was rejected")
	}
	if withinLatencyTolerance(120*time.Millisecond+time.Nanosecond, 100*time.Millisecond, 20) {
		t.Fatal("value beyond boundary was accepted")
	}
}

func TestCostLatencyColdStartReusesDeterministicExploration(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	providers := routingProviders()
	policy := testCostLatencyPolicy(100, 3, time.Minute, 2)
	snapshots := map[string]ProviderTelemetrySnapshot{
		"first":  routingSnapshot(now, repeatedDuration(100*time.Millisecond, 3), nil, nil),
		"second": routingSnapshot(now, repeatedDuration(100*time.Millisecond, 2), nil, nil),
	}
	prices := map[string]accounting.Rates{
		"first": costRates(10, 10), "second": costRates(1, 1),
	}

	assertProviderOrder(t, policy.order(providers, nonStreamingMode, snapshots, prices, now), "first", "second")
	assertProviderOrder(t, policy.order(providers, nonStreamingMode, snapshots, prices, now), "second", "first")

	snapshots["second"] = routingSnapshot(now, repeatedDuration(100*time.Millisecond, 3), nil, nil)
	assertProviderOrder(t, policy.order(providers, nonStreamingMode, snapshots, prices, now), "second", "first")
}

func TestCostLatencyWarmupUsesGreatestFreshModeSpecificDeficit(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	providers := []provider.Provider{
		&recordingProvider{name: "first"},
		&recordingProvider{name: "second"},
		&recordingProvider{name: "third"},
	}
	policy := testCostLatencyPolicy(20, 5, time.Minute, 1)
	snapshots := map[string]ProviderTelemetrySnapshot{
		"first":  routingSnapshot(now, repeatedDuration(100*time.Millisecond, 5), repeatedDuration(10*time.Millisecond, 5), nil),
		"second": routingSnapshot(now, repeatedDuration(100*time.Millisecond, 2), repeatedDuration(10*time.Millisecond, 5), nil),
		"third":  routingSnapshot(now.Add(-2*time.Minute), repeatedDuration(100*time.Millisecond, 5), repeatedDuration(10*time.Millisecond, 5), nil),
	}
	for i := range snapshots["third"].StreamingFirstContentSamples {
		snapshots["third"].StreamingFirstContentSamples[i].ObservedAt = now
	}

	assertProviderOrder(t, policy.order(providers, nonStreamingMode, snapshots, nil, now), "third", "first", "second")
	assertProviderOrder(t, policy.order(providers, streamingMode, snapshots, nil, now), "first", "second", "third")
}

func TestCostLatencyUsesCompletionLatencyAndStreamingTTFC(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	providers := routingProviders()
	policy := testCostLatencyPolicy(20, 3, time.Minute, 10)
	snapshots := map[string]ProviderTelemetrySnapshot{
		"first": routingSnapshot(
			now,
			repeatedDuration(100*time.Millisecond, 3),
			repeatedDuration(200*time.Millisecond, 3),
			repeatedDuration(time.Second, 3),
		),
		"second": routingSnapshot(
			now,
			repeatedDuration(500*time.Millisecond, 3),
			repeatedDuration(210*time.Millisecond, 3),
			repeatedDuration(10*time.Second, 3),
		),
	}
	prices := map[string]accounting.Rates{
		"first": costRates(10, 10), "second": costRates(1, 1),
	}

	assertProviderOrder(t, policy.order(providers, nonStreamingMode, snapshots, prices, now), "first", "second")
	assertProviderOrder(t, policy.order(providers, streamingMode, snapshots, prices, now), "second", "first")
}

func TestCostLatencyResolvesModelsAndHonorsCircuitEligibility(t *testing.T) {
	clock := newManualClock()
	first, second := &recordingProvider{name: "first"}, &recordingProvider{name: "second"}
	service := newCostLatencyTestService(t, clock, 20, 5, 10, first, second)
	seedRoutingTelemetry(service, "first", nonStreamingMode, clock.Now(), 100*time.Millisecond, 5)
	seedRoutingTelemetry(service, "second", nonStreamingMode, clock.Now(), 180*time.Millisecond, 5)
	service.SetPricing(accounting.PriceBook{
		{Provider: "first", Model: "first-model"}:   costRates(1, 1),
		{Provider: "second", Model: "second-model"}: costRates(10, 10),
	})
	attempt, _ := service.health.begin("first")
	attempt.failure()
	req := validRequest()

	response, err := service.Complete(context.Background(), req)
	if err != nil || response.ID != "second" || first.calls != 0 || second.request.Model != "second-model" {
		t.Fatalf("response=%+v error=%v calls=%d,%d request=%+v", response, err, first.calls, second.calls, second.request)
	}
	if req.Model != model.General {
		t.Fatalf("original model = %q", req.Model)
	}

	clock.Advance(time.Minute)
	ordered := service.orderedProviders(nonStreamingMode, req.Model)
	if ordered[0].Name() != "first" {
		t.Fatalf("half-open candidate order = %q", ordered[0].Name())
	}
	health, _ := service.health.snapshot("first")
	if health.State != circuitOpen || health.HalfOpenInFlight {
		t.Fatalf("eligibility inspection reserved trial: %+v", health)
	}
}

func TestCostLatencyFallbackOrderIsStableAndRecordsActualAttempts(t *testing.T) {
	clock := newManualClock()
	first := &recordingProvider{name: "first"}
	second := &recordingProvider{name: "second", err: provider.NewError(provider.ErrorUnavailable, "second", errors.New("unavailable"))}
	service := newCostLatencyTestService(t, clock, 20, 5, 10, first, second)
	seedRoutingTelemetry(service, "first", nonStreamingMode, clock.Now(), 100*time.Millisecond, 5)
	seedRoutingTelemetry(service, "second", nonStreamingMode, clock.Now(), 110*time.Millisecond, 5)
	service.SetPricing(accounting.PriceBook{
		{Provider: "first", Model: "first-model"}:   costRates(10, 10),
		{Provider: "second", Model: "second-model"}: costRates(1, 1),
	})

	response, err := service.Complete(context.Background(), validRequest())
	if err != nil || response.ID != "first" || first.calls != 1 || second.calls != 1 {
		t.Fatalf("response=%+v error=%v calls=%d,%d", response, err, first.calls, second.calls)
	}
	firstTelemetry, _ := service.TelemetrySnapshot("first")
	secondTelemetry, _ := service.TelemetrySnapshot("second")
	if firstTelemetry.Attempts != 1 || secondTelemetry.Attempts != 1 || len(service.AccountingSnapshot().Models) != 2 {
		t.Fatalf("telemetry=%+v,%+v accounting=%+v", firstTelemetry, secondTelemetry, service.AccountingSnapshot())
	}
}

func TestCostLatencyStreamingPreservesFallbackAndCommitment(t *testing.T) {
	t.Run("pre-commit failure falls back", func(t *testing.T) {
		clock := newManualClock()
		first := &streamingTestProvider{name: "first", chunks: []provider.StreamChunk{{Content: "fallback"}}}
		second := &streamingTestProvider{name: "second", err: provider.NewError(provider.ErrorTimeout, "second", errors.New("timeout"))}
		service := newCostLatencyTestService(t, clock, 20, 5, 10, first, second)
		seedRoutingTelemetry(service, "first", streamingMode, clock.Now(), 100*time.Millisecond, 5)
		seedRoutingTelemetry(service, "second", streamingMode, clock.Now(), 110*time.Millisecond, 5)
		service.SetPricing(accounting.PriceBook{
			{Provider: "first", Model: "first-model"}: costRates(10, 10), {Provider: "second", Model: "second-model"}: costRates(1, 1),
		})

		if err := service.Stream(context.Background(), streamRequest(), func(provider.StreamChunk) error { return nil }); err != nil {
			t.Fatalf("Stream() error = %v", err)
		}
		if first.streamCalls != 1 || second.streamCalls != 1 {
			t.Fatalf("stream calls = %d,%d", first.streamCalls, second.streamCalls)
		}
	})

	t.Run("post-commit failure does not fall back", func(t *testing.T) {
		clock := newManualClock()
		first := &streamingTestProvider{name: "first", chunks: []provider.StreamChunk{{Content: "unused"}}}
		second := &streamingTestProvider{
			name: "second", chunks: []provider.StreamChunk{{Content: "partial"}}, errAfter: 1,
			err: provider.NewError(provider.ErrorTimeout, "second", errors.New("timeout")),
		}
		service := newCostLatencyTestService(t, clock, 20, 5, 10, first, second)
		seedRoutingTelemetry(service, "first", streamingMode, clock.Now(), 100*time.Millisecond, 5)
		seedRoutingTelemetry(service, "second", streamingMode, clock.Now(), 110*time.Millisecond, 5)
		service.SetPricing(accounting.PriceBook{
			{Provider: "first", Model: "first-model"}: costRates(10, 10), {Provider: "second", Model: "second-model"}: costRates(1, 1),
		})

		err := service.Stream(context.Background(), streamRequest(), func(provider.StreamChunk) error { return nil })
		if err == nil || first.streamCalls != 0 || second.streamCalls != 1 {
			t.Fatalf("error=%v stream calls=%d,%d", err, first.streamCalls, second.streamCalls)
		}
	})
}

func TestCostLatencyDoesNotAffectExplicitProviders(t *testing.T) {
	for _, providerName := range []string{"openai", "anthropic"} {
		t.Run(providerName, func(t *testing.T) {
			selected := &recordingProvider{name: providerName}
			service := New(selected, testResolver())
			req := openai.ChatCompletionRequest{Model: "native-model", Messages: []openai.Message{{Role: "user"}}}
			if _, err := service.Complete(context.Background(), req); err != nil {
				t.Fatalf("Complete() error = %v", err)
			}
			if selected.calls != 1 || selected.request.Model != req.Model {
				t.Fatalf("selected provider = %+v", selected)
			}
		})
	}
}

func testCostLatencyPolicy(percent uint64, minSamples int, maxAge time.Duration, explorationInterval int) *costLatencyRoutingPolicy {
	return &costLatencyRoutingPolicy{
		latency: &latencyRoutingPolicy{
			minSamples: minSamples, sampleMaxAge: maxAge, explorationInterval: explorationInterval,
		},
		maxLatencyOverFastestPercent: percent,
	}
}

func newCostLatencyTestService(t *testing.T, clock *manualClock, percent uint64, minSamples, explorationInterval int, providers ...provider.Provider) *Service {
	t.Helper()
	service, err := NewAutoWithRouting(testResolver(), CircuitConfig{FailureThreshold: 1, OpenDuration: time.Minute}, RoutingConfig{
		Policy: RoutingPolicyCostLatency, MinSamples: minSamples, SampleMaxAge: time.Minute,
		ExplorationInterval: explorationInterval, MaxLatencyOverFastestPercent: &percent,
	}, providers...)
	if err != nil {
		t.Fatalf("NewAutoWithRouting() error = %v", err)
	}
	names := providerNames(providers)
	service.health = newHealthTracker(names, CircuitConfig{FailureThreshold: 1, OpenDuration: time.Minute}, clock.Now)
	service.telemetry = newTelemetryTracker(names, defaultTelemetrySampleCapacity, clock.Now)
	service.now = clock.Now
	return service
}
