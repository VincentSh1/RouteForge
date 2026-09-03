package observability

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	otelprometheus "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const instrumentationName = "github.com/VincentSh1/RouteForge"

type Config struct {
	TracingEnabled bool
	TraceEndpoint  string
	MetricsEnabled bool
}

type Setup struct {
	tracer         trace.Tracer
	propagator     propagation.TextMapPropagator
	metrics        *Metrics
	metricsHandler http.Handler
	shutdown       []func(context.Context) error
}

func New(ctx context.Context, config Config) (*Setup, error) {
	setup := &Setup{
		tracer:     noop.NewTracerProvider().Tracer(instrumentationName),
		propagator: propagation.TraceContext{},
		metrics:    NoopMetrics(),
	}
	resource := serviceResource()

	if config.TracingEnabled {
		exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(traceEndpoint(config.TraceEndpoint)))
		if err != nil {
			return nil, err
		}
		provider := sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(exporter),
			sdktrace.WithResource(resource),
		)
		setup.tracer = provider.Tracer(instrumentationName)
		setup.shutdown = append(setup.shutdown, provider.Shutdown)
	}

	if config.MetricsEnabled {
		registry := prometheus.NewRegistry()
		exporter, err := otelprometheus.New(
			otelprometheus.WithRegisterer(registry),
			otelprometheus.WithoutScopeInfo(),
			otelprometheus.WithoutTargetInfo(),
		)
		if err != nil {
			_ = setup.Shutdown(ctx)
			return nil, err
		}
		provider := sdkmetric.NewMeterProvider(
			sdkmetric.WithReader(exporter),
			sdkmetric.WithResource(resource),
		)
		metrics, err := NewMetrics(provider.Meter(instrumentationName))
		if err != nil {
			_ = provider.Shutdown(ctx)
			_ = setup.Shutdown(ctx)
			return nil, err
		}
		setup.metrics = metrics
		setup.metricsHandler = promhttp.HandlerFor(registry, promhttp.HandlerOpts{ErrorHandling: promhttp.HTTPErrorOnError})
		setup.shutdown = append(setup.shutdown, provider.Shutdown)
	}
	return setup, nil
}

func (s *Setup) Tracer() trace.Tracer { return s.tracer }

func (s *Setup) Propagator() propagation.TextMapPropagator { return s.propagator }

func (s *Setup) Metrics() *Metrics { return s.metrics }

func (s *Setup) MetricsHandler() http.Handler { return s.metricsHandler }

func (s *Setup) Shutdown(ctx context.Context) error {
	var shutdownErrors []error
	for i := len(s.shutdown) - 1; i >= 0; i-- {
		shutdownErrors = append(shutdownErrors, s.shutdown[i](ctx))
	}
	return errors.Join(shutdownErrors...)
}

func serviceResource() *resource.Resource {
	return resource.NewSchemaless(attribute.String("service.name", "routeforge"))
}

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
