package gateway

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestRuntimeRoutingPreservesStateAndPinsRequests(t *testing.T) {
	s := newLatencyTestServiceWithConfig(t, newManualClock(), 2, time.Minute, 3, &recordingProvider{name: "first"}, &recordingProvider{name: "second"})
	s.routing.exploration.explorationCounts = [2]int{2, 1}
	before := s.OperationsSnapshot(RoutingConfig{})
	health, telemetry, accounting := s.health, s.telemetry, s.accounting
	ctx, name := s.BindRouting(context.Background())
	if name != "latency" {
		t.Fatal("incorrect initial policy")
	}
	percent := uint64(0)
	if _, err := s.UpdateRoutingSettings(RoutingSettings{"cost_latency", 7, &percent}); err != nil {
		t.Fatal(err)
	}
	percent = 50
	if *s.RoutingSettings().MaxLatencyOverFastestPercent != 0 {
		t.Fatal("caller mutated config")
	}
	if s.requestRouting(ctx).config.Policy != "latency" || s.requestRouting(context.Background()).config.Policy != "cost_latency" {
		t.Fatal("request version not pinned")
	}
	after := s.OperationsSnapshot(RoutingConfig{})
	if !reflect.DeepEqual(before.Providers, after.Providers) || !reflect.DeepEqual(before.Routing.ExplorationCounts, after.Routing.ExplorationCounts) || health != s.health || telemetry != s.telemetry || accounting != s.accounting {
		t.Fatal("runtime state reset")
	}
	for _, policy := range []string{"cost", "deterministic", "latency"} {
		if _, err := s.UpdateRoutingSettings(RoutingSettings{policy, 7, nil}); err != nil {
			t.Fatal(err)
		}
	}
	if s.routing.exploration.explorationCounts != [2]int{2, 1} {
		t.Fatal("exploration reset across switches")
	}
}

func TestConcurrentRuntimeRouting(t *testing.T) {
	s := NewAuto(nil, &recordingProvider{name: "first"}, &recordingProvider{name: "second"})
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Go(func() {
			for i := 0; i < 100; i++ {
				policy := "deterministic"
				if i%2 == 0 {
					policy = "latency"
				}
				if _, err := s.UpdateRoutingSettings(RoutingSettings{policy, 5, nil}); err != nil {
					t.Error(err)
				}
				ctx, _ := s.BindRouting(context.Background())
				s.orderedProviders(ctx, nonStreamingMode, "")
				s.OperationsSnapshot(RoutingConfig{})
				s.RoutingSettings()
			}
		})
	}
	wg.Wait()
}

func TestRuntimePolicyChangesProviderOrderImmediately(t *testing.T) {
	clock := newManualClock()
	s := newLatencyTestServiceWithConfig(t, clock, 2, time.Minute, 3, &recordingProvider{name: "first"}, &recordingProvider{name: "second"})
	for _, provider := range []string{"first", "second"} {
		for range 2 {
			attempt := s.telemetry.begin(provider)
			duration := 100 * time.Millisecond
			if provider == "second" {
				duration = 10 * time.Millisecond
			}
			clock.Advance(duration)
			attempt.finishNonStreaming(outcomeSuccess)
		}
	}
	ctx, _ := s.BindRouting(context.Background())
	assertProviderOrder(t, s.orderedProviders(ctx, nonStreamingMode, ""), "second", "first")
	if _, err := s.UpdateRoutingSettings(RoutingSettings{"deterministic", 3, nil}); err != nil {
		t.Fatal(err)
	}
	assertProviderOrder(t, s.orderedProviders(context.Background(), nonStreamingMode, ""), "first", "second")
	assertProviderOrder(t, s.orderedProviders(ctx, nonStreamingMode, ""), "second", "first")
	if _, err := s.UpdateRoutingSettings(RoutingSettings{"latency", 3, nil}); err != nil {
		t.Fatal(err)
	}
	assertProviderOrder(t, s.orderedProviders(context.Background(), nonStreamingMode, ""), "second", "first")
}
