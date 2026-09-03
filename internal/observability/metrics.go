package observability

import (
	"context"
	"math"
	"time"

	"github.com/VincentSh1/RouteForge/internal/openai"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var (
	durationBuckets = []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60, 120, 300}
	ttfcBuckets     = []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60}
)

// Metrics records bounded operational data only. A zero Metrics is a no-op.
type Metrics struct {
	requests           metric.Int64Counter
	requestDuration    metric.Float64Histogram
	routingSelections  metric.Int64Counter
	providerAttempts   metric.Int64Counter
	providerDuration   metric.Float64Histogram
	providerTTFC       metric.Float64Histogram
	fallbacks          metric.Int64Counter
	circuitTransitions metric.Int64Counter
	tokens             metric.Int64Counter
	estimatedCost      metric.Int64Counter
}

func NewMetrics(meter metric.Meter) (*Metrics, error) {
	requests, err := meter.Int64Counter("routeforge_requests", metric.WithDescription("RouteForge chat completion requests"))
	if err != nil {
		return nil, err
	}
	requestDuration, err := meter.Float64Histogram("routeforge_request_duration",
		metric.WithDescription("Full RouteForge request duration"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(durationBuckets...),
	)
	if err != nil {
		return nil, err
	}
	routingSelections, err := meter.Int64Counter("routeforge_routing_selections", metric.WithDescription("Initially selected providers"))
	if err != nil {
		return nil, err
	}
	providerAttempts, err := meter.Int64Counter("routeforge_provider_attempts", metric.WithDescription("Actual provider attempts"))
	if err != nil {
		return nil, err
	}
	providerDuration, err := meter.Float64Histogram("routeforge_provider_duration",
		metric.WithDescription("Provider invocation lifetime"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(durationBuckets...),
	)
	if err != nil {
		return nil, err
	}
	providerTTFC, err := meter.Float64Histogram("routeforge_provider_ttfc",
		metric.WithDescription("Time until first assistant content from a streaming provider"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(ttfcBuckets...),
	)
	if err != nil {
		return nil, err
	}
	fallbacks, err := meter.Int64Counter("routeforge_fallbacks", metric.WithDescription("Fallbacks that reached a subsequent provider attempt"))
	if err != nil {
		return nil, err
	}
	circuitTransitions, err := meter.Int64Counter("routeforge_circuit_transitions", metric.WithDescription("Authoritative provider circuit state transitions"))
	if err != nil {
		return nil, err
	}
	tokens, err := meter.Int64Counter("routeforge_tokens", metric.WithDescription("Authoritative provider-reported tokens"))
	if err != nil {
		return nil, err
	}
	estimatedCost, err := meter.Int64Counter("routeforge_estimated_cost_micro_usd", metric.WithDescription("Configured estimated cost in integer micro-USD"))
	if err != nil {
		return nil, err
	}
	return &Metrics{
		requests: requests, requestDuration: requestDuration,
		routingSelections: routingSelections,
		providerAttempts:  providerAttempts, providerDuration: providerDuration, providerTTFC: providerTTFC,
		fallbacks: fallbacks, circuitTransitions: circuitTransitions,
		tokens: tokens, estimatedCost: estimatedCost,
	}, nil
}

func NoopMetrics() *Metrics { return &Metrics{} }

func (m *Metrics) RecordRequest(ctx context.Context, routingPolicy string, streaming bool, outcome string, duration time.Duration) {
	if m == nil || m.requests == nil {
		return
	}
	attributes := metric.WithAttributes(
		attribute.String("routing_policy", routingPolicy),
		attribute.Bool("streaming", streaming),
		attribute.String("outcome", outcome),
	)
	m.requests.Add(ctx, 1, attributes)
	m.requestDuration.Record(ctx, duration.Seconds(), attributes)
}

func (m *Metrics) RecordRoutingSelection(ctx context.Context, providerName, routingPolicy string, streaming bool) {
	if m == nil || m.routingSelections == nil {
		return
	}
	m.routingSelections.Add(ctx, 1, metric.WithAttributes(
		attribute.String("provider", providerName),
		attribute.String("routing_policy", routingPolicy),
		attribute.Bool("streaming", streaming),
	))
}

func (m *Metrics) RecordProviderAttempt(ctx context.Context, providerName, outcome string, streaming, fallback bool, duration time.Duration) {
	if m == nil || m.providerAttempts == nil {
		return
	}
	m.providerAttempts.Add(ctx, 1, metric.WithAttributes(
		attribute.String("provider", providerName),
		attribute.String("outcome", outcome),
		attribute.Bool("streaming", streaming),
		attribute.Bool("fallback", fallback),
	))
	m.providerDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(
		attribute.String("provider", providerName),
		attribute.Bool("streaming", streaming),
	))
}

func (m *Metrics) RecordTTFC(ctx context.Context, providerName string, duration time.Duration) {
	if m == nil || m.providerTTFC == nil {
		return
	}
	m.providerTTFC.Record(ctx, duration.Seconds(), metric.WithAttributes(attribute.String("provider", providerName)))
}

func (m *Metrics) RecordFallback(ctx context.Context, fromProvider, toProvider, reason string) {
	if m == nil || m.fallbacks == nil {
		return
	}
	m.fallbacks.Add(ctx, 1, metric.WithAttributes(
		attribute.String("from_provider", fromProvider),
		attribute.String("to_provider", toProvider),
		attribute.String("reason", reason),
	))
}

func (m *Metrics) RecordCircuitTransition(providerName, fromState, toState string) {
	if m == nil || m.circuitTransitions == nil {
		return
	}
	m.circuitTransitions.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("provider", providerName),
		attribute.String("from_state", fromState),
		attribute.String("to_state", toState),
	))
}

func (m *Metrics) RecordUsage(ctx context.Context, providerName string, usage *openai.Usage) {
	if m == nil || m.tokens == nil || usage == nil {
		return
	}
	if usage.InputTokens != nil {
		m.addUint64(ctx, m.tokens, *usage.InputTokens, metric.WithAttributes(
			attribute.String("provider", providerName), attribute.String("direction", "input"),
		))
	}
	if usage.OutputTokens != nil {
		m.addUint64(ctx, m.tokens, *usage.OutputTokens, metric.WithAttributes(
			attribute.String("provider", providerName), attribute.String("direction", "output"),
		))
	}
}

func (m *Metrics) RecordEstimatedCost(ctx context.Context, providerName string, microUSD uint64) {
	if m == nil || m.estimatedCost == nil {
		return
	}
	m.addUint64(ctx, m.estimatedCost, microUSD, metric.WithAttributes(attribute.String("provider", providerName)))
}

func (m *Metrics) addUint64(ctx context.Context, counter metric.Int64Counter, value uint64, options ...metric.AddOption) {
	if value > math.MaxInt64 {
		return
	}
	counter.Add(ctx, int64(value), options...)
}
