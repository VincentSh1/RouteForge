package gateway

import "time"

type LatencyState struct {
	StoredSamples int    `json:"stored_samples"`
	FreshSamples  int    `json:"fresh_samples"`
	Sufficient    bool   `json:"sufficient"`
	MedianUS      *int64 `json:"median_us"`
}

type ProviderState struct {
	Provider            string       `json:"provider"`
	CircuitState        string       `json:"circuit_state"`
	OpenUntil           *time.Time   `json:"open_until"`
	Eligible            bool         `json:"eligible"`
	ProbeInFlight       bool         `json:"probe_in_flight"`
	LastSuccess         *time.Time   `json:"last_success"`
	LastFailure         *time.Time   `json:"last_failure"`
	Completion          LatencyState `json:"completion"`
	TTFC                LatencyState `json:"ttfc"`
	PricedModels        int          `json:"priced_models"`
	CompletePriceModels int          `json:"complete_price_models"`
}

type RoutingState struct {
	Policy                       string   `json:"policy"`
	Auto                         bool     `json:"auto"`
	ProviderOrder                []string `json:"provider_order"`
	LatencyAware                 bool     `json:"latency_aware"`
	MinSamples                   int      `json:"min_samples"`
	SampleMaxAgeUS               int64    `json:"sample_max_age_us"`
	ExplorationInterval          int      `json:"exploration_interval"`
	ExplorationCounts            *[2]int  `json:"exploration_counts"`
	MaxLatencyOverFastestPercent *uint64  `json:"max_latency_over_fastest_percent"`
}

type OperationsSnapshot struct {
	ObservedAt time.Time       `json:"observed_at"`
	Routing    RoutingState    `json:"routing"`
	Providers  []ProviderState `json:"providers"`
}

// OperationsSnapshot reads independent, owned copies. It is not a globally
// atomic snapshot and never runs policy ordering, exploration, or admission.
// Config supplies the dormant telemetry thresholds for non-latency policies.
func (s *Service) OperationsSnapshot(config RoutingConfig) OperationsSnapshot {
	now := s.now()
	routing := RoutingState{Policy: s.routingName, Auto: s.fallback, ProviderOrder: providerNames(s.providers),
		MinSamples: config.MinSamples, SampleMaxAgeUS: config.SampleMaxAge.Microseconds(), ExplorationInterval: config.ExplorationInterval}
	var latency *latencyRoutingPolicy
	maxAge := config.SampleMaxAge
	switch policy := s.routing.(type) {
	case *latencyRoutingPolicy:
		latency = policy
	case *costLatencyRoutingPolicy:
		latency = policy.latency
		percent := policy.maxLatencyOverFastestPercent
		routing.MaxLatencyOverFastestPercent = &percent
	}
	if latency != nil {
		maxAge = latency.sampleMaxAge
		routing.LatencyAware = true
		routing.MinSamples = latency.minSamples
		routing.SampleMaxAgeUS = latency.sampleMaxAge.Microseconds()
		routing.ExplorationInterval = latency.explorationInterval
		latency.explorationMu.Lock()
		counts := latency.explorationCounts
		latency.explorationMu.Unlock()
		routing.ExplorationCounts = &counts
	}
	result := OperationsSnapshot{ObservedAt: now.UTC(), Routing: routing, Providers: make([]ProviderState, 0, len(s.providers))}
	for _, item := range s.providers {
		name := item.Name()
		health, _ := s.health.snapshot(name)
		telemetry, _ := s.telemetry.snapshot(name)
		eligible := health.State == circuitClosed || health.State == circuitOpen && !now.Before(health.OpenUntil) || health.State == circuitHalfOpen && !health.HalfOpenInFlight
		state := ProviderState{Provider: name, CircuitState: string(health.State), Eligible: eligible, ProbeInFlight: health.HalfOpenInFlight,
			LastSuccess: optionalTime(telemetry.LastSuccess), LastFailure: optionalTime(telemetry.LastFailure),
			Completion: latencyState(telemetry.NonStreamingLatencySamples, now, routing.MinSamples, maxAge), TTFC: latencyState(telemetry.StreamingFirstContentSamples, now, routing.MinSamples, maxAge)}
		if health.State == circuitOpen {
			state.OpenUntil = optionalTime(health.OpenUntil)
		}
		s.pricingMu.RLock()
		for key, rates := range s.pricing {
			if key.Provider != name {
				continue
			}
			if rates.InputMicroUSDPerMillion != nil || rates.OutputMicroUSDPerMillion != nil {
				state.PricedModels++
			}
			if rates.InputMicroUSDPerMillion != nil && rates.OutputMicroUSDPerMillion != nil {
				state.CompletePriceModels++
			}
		}
		s.pricingMu.RUnlock()
		result.Providers = append(result.Providers, state)
	}
	return result
}

func latencyState(samples []LatencySample, now time.Time, minSamples int, maxAge time.Duration) LatencyState {
	fresh := recentLatencySamples(samples, now, maxAge)
	state := LatencyState{StoredSamples: len(samples), FreshSamples: len(fresh), Sufficient: minSamples > 0 && len(fresh) >= minSamples}
	if state.Sufficient {
		values := make([]time.Duration, len(fresh))
		for i, sample := range fresh {
			values[i] = sample.Duration
		}
		median := medianDuration(values).Microseconds()
		state.MedianUS = &median
	}
	return state
}

func optionalTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	utc := value.UTC()
	return &utc
}
