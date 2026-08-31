package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/VincentSh1/RouteForge/internal/provider"
)

func TestDeterministicRoutingPreservesOrder(t *testing.T) {
	providers := routingProviders()
	policy, ranks, err := newRoutingPolicy(RoutingConfig{Policy: RoutingPolicyDeterministic})
	if err != nil || ranks {
		t.Fatalf("newRoutingPolicy() = %v, %v", ranks, err)
	}
	ordered := policy.order(providers, nonStreamingMode, nil, nil, time.Time{})
	assertProviderOrder(t, ordered, "first", "second")
}

func TestLatencyRoutingRejectsInvalidConfiguration(t *testing.T) {
	for name, config := range map[string]RoutingConfig{
		"unknown policy": {Policy: "fastest", MinSamples: 5, SampleMaxAge: time.Minute, ExplorationInterval: 10},
		"zero samples":   {Policy: RoutingPolicyLatency, SampleMaxAge: time.Minute, ExplorationInterval: 10},
		"zero max age":   {Policy: RoutingPolicyLatency, MinSamples: 5, ExplorationInterval: 10},
		"zero interval":  {Policy: RoutingPolicyLatency, MinSamples: 5, SampleMaxAge: time.Minute},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := newRoutingPolicy(config); err == nil {
				t.Fatal("newRoutingPolicy() error = nil")
			}
		})
	}
}

func TestCostRoutingPolicyIsOptIn(t *testing.T) {
	policy, ranksEligible, err := newRoutingPolicy(RoutingConfig{Policy: RoutingPolicyCost})
	if err != nil {
		t.Fatalf("newRoutingPolicy() error = %v", err)
	}
	if _, ok := policy.(costRoutingPolicy); !ok || !ranksEligible {
		t.Fatalf("newRoutingPolicy() = %T, ranks eligible = %v", policy, ranksEligible)
	}
}

func TestLatencyRoutingUsesModeSpecificMedian(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	policy := testLatencyRoutingPolicy(3, time.Minute)
	providers := routingProviders()
	snapshots := map[string]ProviderTelemetrySnapshot{
		"first":  routingSnapshot(now, []time.Duration{100, 100, 100}, []time.Duration{10, 10, 10}, []time.Duration{1, 1, 1}),
		"second": routingSnapshot(now, []time.Duration{50, 50, 50}, []time.Duration{20, 20, 20}, []time.Duration{time.Nanosecond, time.Nanosecond, time.Nanosecond}),
	}

	assertProviderOrder(t, policy.order(providers, nonStreamingMode, snapshots, nil, now), "second", "first")
	assertProviderOrder(t, policy.order(providers, streamingMode, snapshots, nil, now), "first", "second")
}

func TestLatencyRoutingPreservesOrderForUntrustedData(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	policy := testLatencyRoutingPolicy(3, time.Minute)
	providers := routingProviders()

	tests := map[string]map[string]ProviderTelemetrySnapshot{
		"cold start": {},
		"insufficient samples": {
			"first":  routingSnapshot(now, []time.Duration{100, 100, 100}, nil, nil),
			"second": routingSnapshot(now, []time.Duration{1, 1}, nil, nil),
		},
		"stale samples": {
			"first":  routingSnapshot(now.Add(-2*time.Minute), []time.Duration{100, 100, 100}, nil, nil),
			"second": routingSnapshot(now.Add(-2*time.Minute), []time.Duration{1, 1, 1}, nil, nil),
		},
	}
	mixedAge := routingSnapshot(now, []time.Duration{1}, nil, nil)
	mixedAge.NonStreamingLatencySamples = append(
		latencySamples(now.Add(-2*time.Minute), []time.Duration{1, 1, 1, 1}),
		mixedAge.NonStreamingLatencySamples...,
	)
	tests["old samples do not become fresh"] = map[string]ProviderTelemetrySnapshot{
		"first":  routingSnapshot(now, []time.Duration{100, 100, 100}, nil, nil),
		"second": mixedAge,
	}
	for name, snapshots := range tests {
		t.Run(name, func(t *testing.T) {
			assertProviderOrder(t, policy.order(providers, nonStreamingMode, snapshots, nil, now), "first", "second")
		})
	}
}

func TestLatencyRoutingRequiresMeaningfulImprovement(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	policy := testLatencyRoutingPolicy(3, time.Minute)
	providers := routingProviders()

	snapshots := map[string]ProviderTelemetrySnapshot{
		"first":  routingSnapshot(now, []time.Duration{100, 100, 100}, nil, nil),
		"second": routingSnapshot(now, []time.Duration{91, 91, 91}, nil, nil),
	}
	assertProviderOrder(t, policy.order(providers, nonStreamingMode, snapshots, nil, now), "first", "second")

	snapshots["second"] = routingSnapshot(now, []time.Duration{90, 90, 90}, nil, nil)
	assertProviderOrder(t, policy.order(providers, nonStreamingMode, snapshots, nil, now), "second", "first")
}

func TestLatencyRoutingMedianResistsOutlierAndDoesNotMutateSnapshot(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	policy := testLatencyRoutingPolicy(5, time.Minute)
	providers := routingProviders()
	firstSamples := []time.Duration{100, 100, 100, 100, 100}
	secondSamples := []time.Duration{50, 50, 50, 50, 10_000}
	snapshots := map[string]ProviderTelemetrySnapshot{
		"first":  routingSnapshot(now, firstSamples, nil, nil),
		"second": routingSnapshot(now, secondSamples, nil, nil),
	}

	assertProviderOrder(t, policy.order(providers, nonStreamingMode, snapshots, nil, now), "second", "first")
	if !equalDurations(snapshots["second"].NonStreamingLatencies, secondSamples) {
		t.Fatalf("routing mutated telemetry: %v", snapshots["second"].NonStreamingLatencies)
	}
	if snapshots["second"].NonStreamingLatencySamples[0].Duration != secondSamples[0] {
		t.Fatalf("routing mutated timestamped telemetry: %+v", snapshots["second"].NonStreamingLatencySamples)
	}
	if got := medianDuration([]time.Duration{time.Millisecond, 3 * time.Millisecond}); got != 2*time.Millisecond {
		t.Fatalf("even median = %v", got)
	}
}

func TestLatencyRoutingSkipsOpenProvider(t *testing.T) {
	clock := newManualClock()
	first, second := &recordingProvider{name: "first"}, &recordingProvider{name: "second"}
	service := newLatencyTestService(t, clock, first, second)
	seedRoutingTelemetry(service, "first", nonStreamingMode, clock.Now(), 10*time.Millisecond, 5)
	seedRoutingTelemetry(service, "second", nonStreamingMode, clock.Now(), 100*time.Millisecond, 5)
	attempt, _ := service.health.begin("first")
	attempt.failure()

	response, err := service.Complete(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if response.ID != "second" || first.calls != 0 || second.calls != 1 {
		t.Fatalf("response=%+v calls=%d,%d", response, first.calls, second.calls)
	}
	firstTelemetry, _ := service.TelemetrySnapshot("first")
	if firstTelemetry.Attempts != 0 {
		t.Fatalf("circuit-skipped provider attempts = %d", firstTelemetry.Attempts)
	}
}

func TestLatencyRoutingAllowsOneHalfOpenTrialAfterCooldown(t *testing.T) {
	clock := newManualClock()
	first, second := &recordingProvider{name: "first"}, &recordingProvider{name: "second"}
	service := newLatencyTestService(t, clock, first, second)
	seedRoutingTelemetry(service, "first", nonStreamingMode, clock.Now(), 10*time.Millisecond, 5)
	seedRoutingTelemetry(service, "second", nonStreamingMode, clock.Now(), 100*time.Millisecond, 5)
	attempt, _ := service.health.begin("first")
	attempt.failure()
	clock.Advance(time.Minute)

	response, err := service.Complete(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if response.ID != "first" || first.calls != 1 || second.calls != 0 {
		t.Fatalf("response=%+v calls=%d,%d", response, first.calls, second.calls)
	}
	snapshot, _ := service.health.snapshot("first")
	if snapshot.State != circuitClosed {
		t.Fatalf("circuit state = %q, want %q", snapshot.State, circuitClosed)
	}
}

func TestLatencyOrderedFallbackRemainsStableAndRecordsAttempts(t *testing.T) {
	clock := newManualClock()
	first := &recordingProvider{name: "first"}
	second := &recordingProvider{name: "second", err: provider.NewError(provider.ErrorUnavailable, "second", errors.New("unavailable"))}
	service := newLatencyTestService(t, clock, first, second)
	seedRoutingTelemetry(service, "first", nonStreamingMode, clock.Now(), 100*time.Millisecond, 5)
	seedRoutingTelemetry(service, "second", nonStreamingMode, clock.Now(), 10*time.Millisecond, 5)

	response, err := service.Complete(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if response.ID != "first" || first.calls != 1 || second.calls != 1 {
		t.Fatalf("response=%+v calls=%d,%d", response, first.calls, second.calls)
	}
	firstTelemetry, _ := service.TelemetrySnapshot("first")
	secondTelemetry, _ := service.TelemetrySnapshot("second")
	if firstTelemetry.Attempts != 1 || secondTelemetry.Attempts != 1 {
		t.Fatalf("attempts = %d,%d", firstTelemetry.Attempts, secondTelemetry.Attempts)
	}
}

func TestStreamingLatencyRoutingUsesTTFCAndPreservesCommitment(t *testing.T) {
	t.Run("pre-commit failure falls back", func(t *testing.T) {
		clock := newManualClock()
		first := &streamingTestProvider{name: "first", chunks: []provider.StreamChunk{{Content: "fallback"}}}
		second := &streamingTestProvider{name: "second", err: provider.NewError(provider.ErrorTimeout, "second", errors.New("timeout"))}
		service := newLatencyTestService(t, clock, first, second)
		seedRoutingTelemetry(service, "first", streamingMode, clock.Now(), 100*time.Millisecond, 5)
		seedRoutingTelemetry(service, "second", streamingMode, clock.Now(), 10*time.Millisecond, 5)

		if err := service.Stream(context.Background(), streamRequest(), func(provider.StreamChunk) error { return nil }); err != nil {
			t.Fatalf("Stream() error = %v", err)
		}
		if first.streamCalls != 1 || second.streamCalls != 1 {
			t.Fatalf("stream calls = %d,%d", first.streamCalls, second.streamCalls)
		}
	})

	t.Run("post-commit failure does not fall back", func(t *testing.T) {
		clock := newManualClock()
		first := &streamingTestProvider{name: "first", chunks: []provider.StreamChunk{{Content: "wrong"}}}
		second := &streamingTestProvider{
			name: "second", chunks: []provider.StreamChunk{{Content: "partial"}}, errAfter: 1,
			err: provider.NewError(provider.ErrorTimeout, "second", errors.New("timeout")),
		}
		service := newLatencyTestService(t, clock, first, second)
		seedRoutingTelemetry(service, "first", streamingMode, clock.Now(), 100*time.Millisecond, 5)
		seedRoutingTelemetry(service, "second", streamingMode, clock.Now(), 10*time.Millisecond, 5)

		err := service.Stream(context.Background(), streamRequest(), func(provider.StreamChunk) error { return nil })
		if err == nil || first.streamCalls != 0 || second.streamCalls != 1 {
			t.Fatalf("error=%v stream calls=%d,%d", err, first.streamCalls, second.streamCalls)
		}
	})
}

func newLatencyTestService(t *testing.T, clock *manualClock, providers ...provider.Provider) *Service {
	return newLatencyTestServiceWithConfig(t, clock, 5, time.Minute, 10, providers...)
}

func newLatencyTestServiceWithConfig(t *testing.T, clock *manualClock, minSamples int, sampleMaxAge time.Duration, explorationInterval int, providers ...provider.Provider) *Service {
	t.Helper()
	service, err := NewAutoWithRouting(testResolver(), CircuitConfig{FailureThreshold: 1, OpenDuration: time.Minute}, RoutingConfig{
		Policy:              RoutingPolicyLatency,
		MinSamples:          minSamples,
		SampleMaxAge:        sampleMaxAge,
		ExplorationInterval: explorationInterval,
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

func testLatencyRoutingPolicy(minSamples int, sampleMaxAge time.Duration) *latencyRoutingPolicy {
	return &latencyRoutingPolicy{
		minSamples: minSamples, sampleMaxAge: sampleMaxAge, explorationInterval: 1000,
	}
}

func seedRoutingTelemetry(service *Service, name string, mode requestMode, at time.Time, latency time.Duration, count int) {
	telemetry := service.telemetry.providers[name]
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	for range count {
		if mode == streamingMode {
			telemetry.streamingTTFC.add(latency, at)
		} else {
			telemetry.nonStreamingLatencies.add(latency, at)
		}
	}
}

func routingProviders() []provider.Provider {
	return []provider.Provider{&recordingProvider{name: "first"}, &recordingProvider{name: "second"}}
}

func routingSnapshot(at time.Time, nonStreaming, ttfc, durations []time.Duration) ProviderTelemetrySnapshot {
	return ProviderTelemetrySnapshot{
		NonStreamingLatencies: nonStreaming, StreamingTimeToFirstContent: ttfc, StreamingDurations: durations,
		NonStreamingLatencySamples: latencySamples(at, nonStreaming), StreamingFirstContentSamples: latencySamples(at, ttfc),
	}
}

func latencySamples(at time.Time, durations []time.Duration) []LatencySample {
	samples := make([]LatencySample, len(durations))
	for i, duration := range durations {
		samples[i] = LatencySample{Duration: duration, ObservedAt: at}
	}
	return samples
}

func assertProviderOrder(t *testing.T, providers []provider.Provider, want ...string) {
	t.Helper()
	if len(providers) != len(want) {
		t.Fatalf("provider count = %d, want %d", len(providers), len(want))
	}
	for i, item := range providers {
		if item.Name() != want[i] {
			t.Fatalf("provider[%d] = %q, want %q", i, item.Name(), want[i])
		}
	}
}
