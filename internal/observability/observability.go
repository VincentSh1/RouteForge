package observability

import (
	"context"
	"net/url"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const instrumentationName = "github.com/VincentSh1/RouteForge"

type Setup struct {
	tracer     trace.Tracer
	propagator propagation.TextMapPropagator
	shutdown   func(context.Context) error
}

func New(ctx context.Context, enabled bool, endpoint string) (*Setup, error) {
	propagator := propagation.TraceContext{}
	if !enabled {
		return &Setup{
			tracer:     noop.NewTracerProvider().Tracer(instrumentationName),
			propagator: propagator,
			shutdown:   func(context.Context) error { return nil },
		}, nil
	}

	exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(traceEndpoint(endpoint)))
	if err != nil {
		return nil, err
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", "routeforge"))),
	)
	return &Setup{
		tracer:     provider.Tracer(instrumentationName),
		propagator: propagator,
		shutdown:   provider.Shutdown,
	}, nil
}

func (s *Setup) Tracer() trace.Tracer { return s.tracer }

func (s *Setup) Propagator() propagation.TextMapPropagator { return s.propagator }

func (s *Setup) Shutdown(ctx context.Context) error { return s.shutdown(ctx) }

func traceEndpoint(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = "/v1/traces"
	}
	return parsed.String()
}
