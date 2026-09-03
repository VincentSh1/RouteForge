package gateway

import (
	"context"

	"github.com/VincentSh1/RouteForge/internal/provider"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const gatewayInstrumentationName = "github.com/VincentSh1/RouteForge/internal/gateway"

func noopTracer() trace.Tracer {
	return noop.NewTracerProvider().Tracer(gatewayInstrumentationName)
}

func normalizedRoutingPolicyName(name string) string {
	if name == "" {
		return RoutingPolicyDeterministic
	}
	return name
}

func (s *Service) startProviderAttempt(
	ctx context.Context,
	providerName string,
	providerModel string,
	resolvedLogicalModel bool,
	attemptNumber int,
	streaming bool,
	halfOpen bool,
) (context.Context, trace.Span) {
	if attemptNumber > 1 {
		trace.SpanFromContext(ctx).AddEvent("routeforge.fallback", trace.WithAttributes(
			attribute.String("routeforge.provider.name", providerName),
			attribute.Int("routeforge.provider.attempt_number", attemptNumber),
		))
	}
	attributes := []attribute.KeyValue{
		attribute.String("routeforge.provider.name", providerName),
		attribute.Int("routeforge.provider.attempt_number", attemptNumber),
		attribute.Bool("routeforge.provider.fallback", attemptNumber > 1),
		attribute.Bool("routeforge.request.streaming", streaming),
		attribute.Bool("routeforge.circuit.half_open_trial", halfOpen),
	}
	// Configured logical-model mappings are bounded operational data. Arbitrary
	// client-supplied native model names are omitted to avoid unbounded cardinality.
	if resolvedLogicalModel {
		attributes = append(attributes, attribute.String("routeforge.provider.model", providerModel))
	}
	return s.tracer.Start(ctx, "routeforge.provider.attempt", trace.WithAttributes(attributes...))
}

func finishProviderAttempt(span trace.Span, outcome providerOutcome) {
	span.SetAttributes(attribute.String("routeforge.provider.outcome", outcome.String()))
	if outcome != outcomeSuccess && outcome != outcomeCanceled {
		span.SetStatus(codes.Error, outcome.String())
	}
	span.End()
}

func recordCircuitSkip(ctx context.Context, providerName string) {
	trace.SpanFromContext(ctx).AddEvent("routeforge.provider.skipped", trace.WithAttributes(
		attribute.String("routeforge.provider.name", providerName),
		attribute.String("routeforge.skip.reason", "circuit_admission_denied"),
	))
}

func recordRoutingResult(span trace.Span, ordered []provider.Provider, eligible int) {
	span.SetAttributes(
		attribute.Int("routeforge.routing.eligible_candidates", eligible),
		attribute.Int("routeforge.routing.ordered_candidates", len(ordered)),
	)
	if eligible > 0 && len(ordered) > 0 {
		span.SetAttributes(attribute.String("routeforge.routing.initial_provider", ordered[0].Name()))
	}
}
