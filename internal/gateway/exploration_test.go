package gateway

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VincentSh1/RouteForge/internal/provider"
)

func TestLatencyExplorationWarmsAlternateAtConfiguredInterval(t *testing.T) {
	clock := newManualClock()
	first, second := &recordingProvider{name: "first"}, &recordingProvider{name: "second"}
	service := newLatencyTestServiceWithConfig(t, clock, 5, time.Minute, 3, first, second)

	for range 2 {
		response, err := service.Complete(context.Background(), validRequest())
		if err != nil || response.ID != "first" {
			t.Fatalf("non-exploration response = %+v, error = %v", response, err)
		}
	}
	response, err := service.Complete(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("exploration Complete() error = %v", err)
	}
	if response.ID != "second" || first.calls != 2 || second.calls != 1 {
		t.Fatalf("response=%+v calls=%d,%d", response, first.calls, second.calls)
	}
}

func TestExplorationDoesNotAffectDeterministicOrExplicitRouting(t *testing.T) {
	t.Run("deterministic auto", func(t *testing.T) {
		first, second := &recordingProvider{name: "first"}, &recordingProvider{name: "second"}
		service := NewAuto(testResolver(), first, second)
		for range 20 {
			if _, err := service.Complete(context.Background(), validRequest()); err != nil {
				t.Fatalf("Complete() error = %v", err)
			}
		}
		if first.calls != 20 || second.calls != 0 {
			t.Fatalf("calls = %d,%d", first.calls, second.calls)
		}
	})

	t.Run("explicit provider", func(t *testing.T) {
		selected := &recordingProvider{name: "first"}
		service := New(selected, testResolver())
		for range 20 {
			if _, err := service.Complete(context.Background(), validRequest()); err != nil {
				t.Fatalf("Complete() error = %v", err)
			}
		}
		if selected.calls != 20 {
			t.Fatalf("calls = %d", selected.calls)
		}
	})
}

func TestExplorationCadenceIsDeterministic(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	policy := &latencyRoutingPolicy{minSamples: 5, sampleMaxAge: time.Minute, explorationInterval: 3}
	providers := routingProviders()
	snapshots := map[string]ProviderTelemetrySnapshot{
		"first":  routingSnapshot(now, repeatedDuration(100*time.Millisecond, 5), nil, nil),
		"second": routingSnapshot(now, nil, nil, nil),
	}
	wantFirst := []string{"first", "first", "second", "first", "first", "second"}
	for i, want := range wantFirst {
		ordered := policy.order(providers, nonStreamingMode, snapshots, now)
		if ordered[0].Name() != want {
			t.Fatalf("request %d first provider = %q, want %q", i+1, ordered[0].Name(), want)
		}
	}
}

func TestExplorationSelectsLargestDeficitWithStableTieBreak(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	providers := []provider.Provider{
		&recordingProvider{name: "first"},
		&recordingProvider{name: "second"},
		&recordingProvider{name: "third"},
	}

	t.Run("largest deficit", func(t *testing.T) {
		policy := &latencyRoutingPolicy{minSamples: 5, sampleMaxAge: time.Minute, explorationInterval: 1}
		snapshots := map[string]ProviderTelemetrySnapshot{
			"first":  routingSnapshot(now, repeatedDuration(100*time.Millisecond, 5), nil, nil),
			"second": routingSnapshot(now, repeatedDuration(100*time.Millisecond, 2), nil, nil),
			"third":  routingSnapshot(now, repeatedDuration(100*time.Millisecond, 1), nil, nil),
		}
		assertProviderOrder(t, policy.order(providers, nonStreamingMode, snapshots, now), "third", "first", "second")
	})

	t.Run("configured order breaks ties", func(t *testing.T) {
		policy := &latencyRoutingPolicy{minSamples: 5, sampleMaxAge: time.Minute, explorationInterval: 1}
		snapshots := map[string]ProviderTelemetrySnapshot{
			"first":  routingSnapshot(now, repeatedDuration(100*time.Millisecond, 5), nil, nil),
			"second": routingSnapshot(now, repeatedDuration(100*time.Millisecond, 1), nil, nil),
			"third":  routingSnapshot(now, repeatedDuration(100*time.Millisecond, 1), nil, nil),
		}
		assertProviderOrder(t, policy.order(providers, nonStreamingMode, snapshots, now), "second", "first", "third")
	})
}

func TestExplorationUsesFreshModeSpecificSamples(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	providers := routingProviders()
	firstComplete := repeatedDuration(100*time.Millisecond, 5)
	firstTTFC := repeatedDuration(10*time.Millisecond, 5)
	secondComplete := repeatedDuration(200*time.Millisecond, 5)
	secondTTFC := repeatedDuration(20*time.Millisecond, 5)

	t.Run("streaming samples do not warm non-streaming", func(t *testing.T) {
		policy := &latencyRoutingPolicy{minSamples: 5, sampleMaxAge: time.Minute, explorationInterval: 1}
		snapshots := map[string]ProviderTelemetrySnapshot{
			"first":  routingSnapshot(now, firstComplete, firstTTFC, nil),
			"second": routingSnapshot(now, nil, secondTTFC, nil),
		}
		assertProviderOrder(t, policy.order(providers, nonStreamingMode, snapshots, now), "second", "first")
	})

	t.Run("non-streaming samples do not warm streaming", func(t *testing.T) {
		policy := &latencyRoutingPolicy{minSamples: 5, sampleMaxAge: time.Minute, explorationInterval: 1}
		snapshots := map[string]ProviderTelemetrySnapshot{
			"first":  routingSnapshot(now, firstComplete, firstTTFC, nil),
			"second": routingSnapshot(now, secondComplete, nil, nil),
		}
		assertProviderOrder(t, policy.order(providers, streamingMode, snapshots, now), "second", "first")
	})

	t.Run("stale samples resume warm-up", func(t *testing.T) {
		policy := &latencyRoutingPolicy{minSamples: 5, sampleMaxAge: time.Minute, explorationInterval: 1}
		snapshots := map[string]ProviderTelemetrySnapshot{
			"first":  routingSnapshot(now, firstComplete, nil, nil),
			"second": routingSnapshot(now.Add(-2*time.Minute), secondComplete, nil, nil),
		}
		assertProviderOrder(t, policy.order(providers, nonStreamingMode, snapshots, now), "second", "first")
	})
}

func TestExplorationCountersAreIndependentByRequestMode(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	policy := &latencyRoutingPolicy{minSamples: 5, sampleMaxAge: time.Minute, explorationInterval: 2}
	providers := routingProviders()
	snapshots := map[string]ProviderTelemetrySnapshot{
		"first": routingSnapshot(
			now,
			repeatedDuration(100*time.Millisecond, 5),
			repeatedDuration(10*time.Millisecond, 5),
			nil,
		),
		"second": routingSnapshot(now, nil, nil, nil),
	}

	assertProviderOrder(t, policy.order(providers, nonStreamingMode, snapshots, now), "first", "second")
	assertProviderOrder(t, policy.order(providers, streamingMode, snapshots, now), "first", "second")
	assertProviderOrder(t, policy.order(providers, nonStreamingMode, snapshots, now), "second", "first")
	assertProviderOrder(t, policy.order(providers, streamingMode, snapshots, now), "second", "first")
}

func TestExplorationStopsWhenWarmAndLatencyRankingResumes(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	policy := &latencyRoutingPolicy{minSamples: 5, sampleMaxAge: time.Minute, explorationInterval: 1}
	providers := routingProviders()
	snapshots := map[string]ProviderTelemetrySnapshot{
		"first":  routingSnapshot(now, repeatedDuration(100*time.Millisecond, 5), nil, nil),
		"second": routingSnapshot(now, repeatedDuration(90*time.Millisecond, 5), nil, nil),
	}

	for range 3 {
		assertProviderOrder(t, policy.order(providers, nonStreamingMode, snapshots, now), "second", "first")
	}
	snapshots["second"] = routingSnapshot(now, repeatedDuration(91*time.Millisecond, 5), nil, nil)
	assertProviderOrder(t, policy.order(providers, nonStreamingMode, snapshots, now), "first", "second")
}

func TestCircuitOpenProviderIsNeverExplored(t *testing.T) {
	clock := newManualClock()
	first, second := &recordingProvider{name: "first"}, &recordingProvider{name: "second"}
	service := newLatencyTestServiceWithConfig(t, clock, 5, time.Minute, 1, first, second)
	seedRoutingTelemetry(service, "first", nonStreamingMode, clock.Now(), 100*time.Millisecond, 5)
	attempt, _ := service.health.begin("second")
	attempt.failure()

	if _, err := service.Complete(context.Background(), validRequest()); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if first.calls != 1 || second.calls != 0 {
		t.Fatalf("calls = %d,%d", first.calls, second.calls)
	}
}

func TestExplorationSelectedProviderFallsBackNormally(t *testing.T) {
	clock := newManualClock()
	first := &recordingProvider{name: "first"}
	second := &recordingProvider{name: "second", err: provider.NewError(provider.ErrorUnavailable, "second", errors.New("unavailable"))}
	service := newLatencyTestServiceWithConfig(t, clock, 5, time.Minute, 1, first, second)
	seedRoutingTelemetry(service, "first", nonStreamingMode, clock.Now(), 100*time.Millisecond, 5)

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
		t.Fatalf("telemetry attempts = %d,%d", firstTelemetry.Attempts, secondTelemetry.Attempts)
	}
}

func TestStreamingExplorationWarmsTTFC(t *testing.T) {
	clock := newManualClock()
	first := &streamingTestProvider{name: "first", chunks: []provider.StreamChunk{{Content: "first"}}}
	second := &streamingTestProvider{name: "second", chunks: []provider.StreamChunk{{Content: "second"}}}
	service := newLatencyTestServiceWithConfig(t, clock, 5, time.Minute, 1, first, second)
	seedRoutingTelemetry(service, "first", streamingMode, clock.Now(), 10*time.Millisecond, 5)

	if err := service.Stream(context.Background(), streamRequest(), func(provider.StreamChunk) error { return nil }); err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	if first.streamCalls != 0 || second.streamCalls != 1 {
		t.Fatalf("stream calls = %d,%d", first.streamCalls, second.streamCalls)
	}
	snapshot, _ := service.TelemetrySnapshot("second")
	if len(snapshot.StreamingFirstContentSamples) != 1 || len(snapshot.NonStreamingLatencySamples) != 0 {
		t.Fatalf("second telemetry = %+v", snapshot)
	}
}

func TestConcurrentExplorationCadenceIsBounded(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	policy := &latencyRoutingPolicy{minSamples: 5, sampleMaxAge: time.Minute, explorationInterval: 10}
	providers := routingProviders()
	snapshots := map[string]ProviderTelemetrySnapshot{
		"first":  routingSnapshot(now, repeatedDuration(100*time.Millisecond, 5), nil, nil),
		"second": routingSnapshot(now, nil, nil, nil),
	}

	const requests = 100
	var explorations atomic.Int32
	var wait sync.WaitGroup
	for range requests {
		wait.Add(1)
		go func() {
			defer wait.Done()
			ordered := policy.order(providers, nonStreamingMode, snapshots, now)
			if ordered[0].Name() == "second" {
				explorations.Add(1)
			}
		}()
	}
	wait.Wait()
	if got := explorations.Load(); got != requests/10 {
		t.Fatalf("explorations = %d, want %d", got, requests/10)
	}
}

func repeatedDuration(value time.Duration, count int) []time.Duration {
	values := make([]time.Duration, count)
	for i := range values {
		values[i] = value
	}
	return values
}
