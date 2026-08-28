package gateway

import (
	"fmt"
	"sort"
	"time"

	"github.com/VincentSh1/RouteForge/internal/provider"
)

const (
	RoutingPolicyDeterministic = "deterministic"
	RoutingPolicyLatency       = "latency"

	routingSwitchMarginDivisor = 10 // An alternative must be at least 10% faster.
)

type RoutingConfig struct {
	Policy       string
	MinSamples   int
	SampleMaxAge time.Duration
}

type requestMode uint8

const (
	nonStreamingMode requestMode = iota
	streamingMode
)

type routingPolicy interface {
	order([]provider.Provider, requestMode, map[string]ProviderTelemetrySnapshot, time.Time) []provider.Provider
}

type deterministicRoutingPolicy struct{}

func (deterministicRoutingPolicy) order(candidates []provider.Provider, _ requestMode, _ map[string]ProviderTelemetrySnapshot, _ time.Time) []provider.Provider {
	return append([]provider.Provider(nil), candidates...)
}

type latencyRoutingPolicy struct {
	minSamples   int
	sampleMaxAge time.Duration
}

func newRoutingPolicy(config RoutingConfig) (routingPolicy, bool, error) {
	switch config.Policy {
	case "", RoutingPolicyDeterministic:
		return deterministicRoutingPolicy{}, false, nil
	case RoutingPolicyLatency:
		if config.MinSamples <= 0 {
			return nil, false, fmt.Errorf("routing minimum samples must be positive")
		}
		if config.SampleMaxAge <= 0 {
			return nil, false, fmt.Errorf("routing sample maximum age must be positive")
		}
		return latencyRoutingPolicy{minSamples: config.MinSamples, sampleMaxAge: config.SampleMaxAge}, true, nil
	default:
		return nil, false, fmt.Errorf("unknown routing policy")
	}
}

func (p latencyRoutingPolicy) order(candidates []provider.Provider, mode requestMode, snapshots map[string]ProviderTelemetrySnapshot, now time.Time) []provider.Provider {
	ordered := append([]provider.Provider(nil), candidates...)
	if len(ordered) < 2 {
		return ordered
	}

	medians := make([]time.Duration, len(ordered))
	for i, candidate := range ordered {
		snapshot, ok := snapshots[candidate.Name()]
		if !ok {
			return ordered
		}
		samples := recentLatencySamples(relevantLatency(snapshot, mode), now, p.sampleMaxAge)
		if len(samples) < p.minSamples {
			return ordered
		}
		durations := make([]time.Duration, len(samples))
		for j, sample := range samples {
			durations[j] = sample.Duration
		}
		medians[i] = medianDuration(durations)
	}

	best := 0
	for i := 1; i < len(medians); i++ {
		if medians[i] < medians[best] {
			best = i
		}
	}
	if best == 0 || medians[best] > medians[0]-medians[0]/routingSwitchMarginDivisor {
		return ordered
	}

	selected := ordered[best]
	copy(ordered[1:best+1], ordered[0:best])
	ordered[0] = selected
	return ordered
}

func relevantLatency(snapshot ProviderTelemetrySnapshot, mode requestMode) []LatencySample {
	if mode == streamingMode {
		return snapshot.StreamingFirstContentSamples
	}
	return snapshot.NonStreamingLatencySamples
}

func recentLatencySamples(samples []LatencySample, now time.Time, maxAge time.Duration) []LatencySample {
	cutoff := now.Add(-maxAge)
	recent := make([]LatencySample, 0, len(samples))
	for _, sample := range samples {
		if !sample.ObservedAt.Before(cutoff) {
			recent = append(recent, sample)
		}
	}
	return recent
}

func medianDuration(samples []time.Duration) time.Duration {
	values := append([]time.Duration(nil), samples...)
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	middle := len(values) / 2
	if len(values)%2 != 0 {
		return values[middle]
	}
	lower, upper := values[middle-1], values[middle]
	return lower + (upper-lower)/2
}
