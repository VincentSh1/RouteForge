package gateway

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VincentSh1/RouteForge/internal/accounting"
)

func TestOperationsSnapshotDoesNotMutateState(t *testing.T) {
	clock := newManualClock()
	p := &recordingProvider{name: "first"}
	s := newLatencyTestServiceWithConfig(t, clock, 2, time.Minute, 3, p)
	for range s.health.failureThreshold {
		a, ok := s.health.begin("first")
		if !ok {
			t.Fatal("admission failed")
		}
		a.failure()
	}
	clock.Advance(time.Minute)
	before, _ := s.health.snapshot("first")
	telemetry, _ := s.telemetry.snapshot("first")
	policy := s.routing.(*latencyRoutingPolicy)
	counts := policy.explorationCounts
	for range 20 {
		view := s.OperationsSnapshot(RoutingConfig{})
		if view.Providers[0].CircuitState != "open" || !view.Providers[0].Eligible || view.Providers[0].ProbeInFlight {
			t.Fatal("expired open state was not reported truthfully")
		}
		view.Routing.ProviderOrder[0] = "changed"
		view.Routing.ExplorationCounts[0] = 100
		view.Providers[0].Provider = "changed"
	}
	after, _ := s.health.snapshot("first")
	afterTelemetry, _ := s.telemetry.snapshot("first")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(telemetry, afterTelemetry) || counts != policy.explorationCounts || p.calls != 0 {
		t.Fatal("inspection mutated state")
	}
	probe, ok := s.health.begin("first")
	if !ok {
		t.Fatal("inspection reserved the probe")
	}
	view := s.OperationsSnapshot(RoutingConfig{})
	if view.Providers[0].CircuitState != "half_open" || view.Providers[0].Eligible || !view.Providers[0].ProbeInFlight {
		t.Fatal("in-flight half-open state incorrect")
	}
	probe.ignore()
	if !s.OperationsSnapshot(RoutingConfig{}).Providers[0].Eligible {
		t.Fatal("unreserved half-open must be eligible")
	}
	probe, ok = s.health.begin("first")
	if !ok {
		t.Fatal("missing probe")
	}
	probe.success()
	if s.OperationsSnapshot(RoutingConfig{}).Providers[0].CircuitState != "closed" {
		t.Fatal("closed state missing")
	}
}

func TestOperationsTelemetryFreshnessPricingAndPrivacy(t *testing.T) {
	clock := newManualClock()
	s := newLatencyTestServiceWithConfig(t, clock, 2, time.Minute, 3, &recordingProvider{name: "first"})
	zero := uint64(0)
	s.SetPricing(accounting.PriceBook{{Provider: "first", Model: "sensitive-model-sentinel"}: {InputMicroUSDPerMillion: &zero, OutputMicroUSDPerMillion: &zero},
		{Provider: "first", Model: "partial"}: {InputMicroUSDPerMillion: &zero}})
	for i := 0; i < 2; i++ {
		a := s.telemetry.begin("first")
		clock.Advance(time.Millisecond)
		a.finishNonStreaming(outcomeSuccess)
		view := s.OperationsSnapshot(RoutingConfig{})
		if view.Providers[0].Completion.Sufficient != (i == 1) {
			t.Fatal("sample threshold ignored")
		}
	}
	view := s.OperationsSnapshot(RoutingConfig{})
	p := view.Providers[0]
	if p.Completion.MedianUS == nil || *p.Completion.MedianUS != 1000 || p.TTFC.MedianUS != nil || p.TTFC.Sufficient || p.LastSuccess == nil || p.LastFailure != nil || p.CompletePriceModels != 1 || p.PricedModels != 2 {
		t.Fatal("incorrect telemetry or pricing snapshot")
	}
	data, _ := json.Marshal(view)
	for _, secret := range []string{"sensitive-model-sentinel", "InputMicroUSDPerMillion", "prompt", "credential", "redis", "request_id"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("snapshot leaked private state")
		}
	}
	clock.Advance(time.Minute + time.Nanosecond)
	p = s.OperationsSnapshot(RoutingConfig{}).Providers[0]
	if p.Completion.FreshSamples != 0 || p.Completion.StoredSamples != 2 || p.Completion.MedianUS != nil {
		t.Fatal("stale telemetry treated as current")
	}
	s.SetPricing(nil)
	if s.OperationsSnapshot(RoutingConfig{}).Providers[0].PricedModels != 0 {
		t.Fatal("missing pricing misrepresented")
	}
}

func TestOperationsConcurrentInspection(t *testing.T) {
	s := New(&recordingProvider{name: "first"}, testResolver())
	config := RoutingConfig{MinSamples: 2, SampleMaxAge: time.Minute, ExplorationInterval: 3}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 30 {
				a, ok := s.health.begin("first")
				if ok {
					a.success()
				}
				attempt := s.telemetry.begin("first")
				attempt.finishNonStreaming(outcomeSuccess)
				_ = s.OperationsSnapshot(config)
			}
		}()
	}
	wg.Wait()
	view := s.OperationsSnapshot(config)
	if view.Routing.LatencyAware || view.Routing.Auto || view.Routing.ExplorationCounts != nil {
		t.Fatal("explicit routing reported as latency exploration")
	}
}

func TestOperationsActiveCostLatencySettings(t *testing.T) {
	percent := uint64(20)
	s, err := NewAutoWithRouting(testResolver(), CircuitConfig{FailureThreshold: 1, OpenDuration: time.Minute}, RoutingConfig{Policy: RoutingPolicyCostLatency, MinSamples: 2, SampleMaxAge: time.Minute, ExplorationInterval: 3, MaxLatencyOverFastestPercent: &percent}, &recordingProvider{name: "first"})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.health.begin("first")
	a.failure()
	view := s.OperationsSnapshot(RoutingConfig{})
	if view.Providers[0].Eligible || view.Providers[0].OpenUntil == nil || view.Routing.MaxLatencyOverFastestPercent == nil || *view.Routing.MaxLatencyOverFastestPercent != 20 || !view.Routing.LatencyAware {
		t.Fatal("active settings or cooldown incorrect")
	}
}
