package gateway

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

// RoutingSettings is the entire supported runtime write surface. Thresholds,
// providers, model mappings and pricing remain startup-only configuration.
type RoutingSettings struct {
	Policy                       string  `json:"policy"`
	ExplorationInterval          int     `json:"exploration_interval"`
	MaxLatencyOverFastestPercent *uint64 `json:"max_latency_over_fastest_percent"`
}

type routingVersion struct {
	config       RoutingConfig
	policy       routingPolicy
	rankEligible bool
}
type runtimeRouting struct {
	current     atomic.Pointer[routingVersion]
	exploration *explorationState
}
type routingContextKey struct{ service *Service }

func newRuntimeRouting(config RoutingConfig, policy routingPolicy, rank bool) *runtimeRouting {
	config.Policy = normalizedRoutingPolicyName(config.Policy)
	if config.MinSamples == 0 {
		config.MinSamples = 5
	}
	if config.SampleMaxAge == 0 {
		config.SampleMaxAge = 5 * time.Minute
	}
	if config.ExplorationInterval == 0 {
		config.ExplorationInterval = 10
	}
	config = cloneRoutingConfig(config)
	state := &runtimeRouting{exploration: &explorationState{}}
	state.publish(config, policy, rank)
	return state
}

func cloneRoutingConfig(config RoutingConfig) RoutingConfig {
	if config.MaxLatencyOverFastestPercent != nil {
		value := *config.MaxLatencyOverFastestPercent
		config.MaxLatencyOverFastestPercent = &value
	}
	return config
}

func (state *runtimeRouting) publish(config RoutingConfig, policy routingPolicy, rank bool) {
	switch p := policy.(type) {
	case *latencyRoutingPolicy:
		p.explorationState = state.exploration
	case *costLatencyRoutingPolicy:
		p.latency.explorationState = state.exploration
	}
	state.current.Store(&routingVersion{config: config, policy: policy, rankEligible: rank})
}

// ConfigureRouting is startup-only; runtime writes use UpdateRoutingSettings.
func (s *Service) ConfigureRouting(config RoutingConfig) error {
	policy, rank, err := newRoutingPolicy(config)
	if err != nil {
		return err
	}
	s.routing.publish(cloneRoutingConfig(config), policy, rank)
	return nil
}

func (s *Service) RoutingSettings() RoutingSettings {
	config := cloneRoutingConfig(s.routing.current.Load().config)
	return RoutingSettings{config.Policy, config.ExplorationInterval, config.MaxLatencyOverFastestPercent}
}

func (s *Service) UpdateRoutingSettings(settings RoutingSettings) (RoutingSettings, error) {
	// Integer bounds keep the operator surface useful and JSON interoperable.
	if settings.ExplorationInterval < 1 || settings.ExplorationInterval > 1000000 ||
		settings.MaxLatencyOverFastestPercent != nil && *settings.MaxLatencyOverFastestPercent > 1000000 {
		return RoutingSettings{}, errors.New("invalid routing settings")
	}
	switch settings.Policy {
	case RoutingPolicyDeterministic, RoutingPolicyLatency, RoutingPolicyCost, RoutingPolicyCostLatency:
	default:
		return RoutingSettings{}, errors.New("invalid routing policy")
	}
	config := cloneRoutingConfig(s.routing.current.Load().config)
	config.Policy, config.ExplorationInterval = settings.Policy, settings.ExplorationInterval
	config.MaxLatencyOverFastestPercent = settings.MaxLatencyOverFastestPercent
	config = cloneRoutingConfig(config)
	policy, rank, err := newRoutingPolicy(config)
	if err != nil {
		return RoutingSettings{}, errors.New("invalid routing settings")
	}
	s.routing.publish(config, policy, rank)
	// Return this write's values, not a later concurrent writer's values.
	return RoutingSettings{config.Policy, config.ExplorationInterval, cloneRoutingConfig(config).MaxLatencyOverFastestPercent}, nil
}

// BindRouting pins one immutable version across routing, history and HTTP
// instrumentation, including streams still running when settings change.
func (s *Service) BindRouting(ctx context.Context) (context.Context, string) {
	version, ok := ctx.Value(routingContextKey{s}).(*routingVersion)
	if !ok {
		version = s.routing.current.Load()
		ctx = context.WithValue(ctx, routingContextKey{s}, version)
	}
	return ctx, version.config.Policy
}

func (s *Service) requestRouting(ctx context.Context) *routingVersion {
	if version, ok := ctx.Value(routingContextKey{s}).(*routingVersion); ok {
		return version
	}
	return s.routing.current.Load()
}
