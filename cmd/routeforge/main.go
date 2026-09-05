package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/VincentSh1/RouteForge/internal/accounting"
	"github.com/VincentSh1/RouteForge/internal/config"
	"github.com/VincentSh1/RouteForge/internal/gateway"
	"github.com/VincentSh1/RouteForge/internal/httpapi"
	"github.com/VincentSh1/RouteForge/internal/model"
	"github.com/VincentSh1/RouteForge/internal/observability"
	"github.com/VincentSh1/RouteForge/internal/persistence"
	postgrespersistence "github.com/VincentSh1/RouteForge/internal/persistence/postgres"
	"github.com/VincentSh1/RouteForge/internal/provider"
	"github.com/VincentSh1/RouteForge/internal/provider/anthropic"
	"github.com/VincentSh1/RouteForge/internal/provider/mock"
	openaiadapter "github.com/VincentSh1/RouteForge/internal/provider/openai"
)

func main() {
	if err := run(); err != nil {
		var validationErr *config.ValidationError
		if errors.As(err, &validationErr) {
			slog.Error("invalid configuration", "reason", validationErr.Error())
		} else {
			slog.Error("RouteForge stopped")
		}
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	observabilitySetup, err := observability.New(context.Background(), observability.Config{
		TracingEnabled: cfg.OTelEnabled,
		TraceEndpoint:  cfg.OTelExporterOTLPEndpoint,
		MetricsEnabled: cfg.MetricsEnabled,
	})
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := observabilitySetup.Shutdown(shutdownCtx); err != nil {
			slog.Warn("OpenTelemetry shutdown incomplete")
		}
	}()

	historyRecorder, historyShutdown, err := initializePersistence(cfg, observabilitySetup.Metrics())
	if err != nil {
		return err
	}
	if historyShutdown != nil {
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
			defer cancel()
			if err := historyShutdown(shutdownCtx); err != nil {
				slog.Warn("PostgreSQL persistence shutdown incomplete")
			}
		}()
	}

	service, err := buildService(cfg)
	if err != nil {
		return err
	}
	service.SetTracer(observabilitySetup.Tracer())
	service.SetMetrics(observabilitySetup.Metrics())
	service.SetPersistence(historyRecorder, nil)
	handler := httpapi.NewHandler(service)
	routes := httpapi.TraceRequests(handler.Routes(), observabilitySetup.Tracer(), observabilitySetup.Propagator(), cfg.RoutingPolicy, observabilitySetup.Metrics())
	server := httpapi.NewServer(cfg, routes)
	servers := []*http.Server{server}
	if cfg.MetricsEnabled {
		metricsMux := http.NewServeMux()
		metricsMux.Handle("/metrics", observabilitySetup.MetricsHandler())
		servers = append(servers, httpapi.NewMetricsServer(cfg, metricsMux))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	listeners := make([]net.Listener, 0, len(servers))
	for i, item := range servers {
		listener, err := net.Listen("tcp", item.Addr)
		if err != nil {
			for _, opened := range listeners {
				_ = opened.Close()
			}
			if i == 0 {
				return fmt.Errorf("API listener failed to start")
			}
			return fmt.Errorf("metrics listener failed to start")
		}
		listeners = append(listeners, listener)
	}

	serverErr := make(chan error, len(servers))
	for i, item := range servers {
		go func(server *http.Server, listener net.Listener) {
			serverErr <- server.Serve(listener)
		}(item, listeners[i])
	}
	slog.Info("RouteForge listening")

	select {
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
			defer cancel()
			for _, item := range servers {
				_ = item.Shutdown(shutdownCtx)
			}
			return err
		}
		return nil
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	var shutdownErrors []error
	for _, item := range servers {
		shutdownErrors = append(shutdownErrors, item.Shutdown(shutdownCtx))
	}
	return errors.Join(shutdownErrors...)
}

func initializePersistence(cfg config.Config, metrics *observability.Metrics) (persistence.Recorder, func(context.Context) error, error) {
	if !cfg.PostgresEnabled {
		return persistence.NoopRecorder{}, nil, nil
	}
	startupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := postgrespersistence.Open(startupCtx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, errors.New("PostgreSQL persistence initialization failed")
	}
	recorder := persistence.NewAsyncRecorder(store, persistence.DefaultQueueCapacity, func(outcome persistence.WriteOutcome) {
		metrics.RecordPersistence(context.Background(), string(outcome))
	})
	return recorder, recorder.Shutdown, nil
}

func buildService(cfg config.Config) (*gateway.Service, error) {
	client := &http.Client{Timeout: cfg.ProviderTimeout}
	providers := []provider.Provider{&mock.Provider{}}
	if strings.TrimSpace(cfg.OpenAIAPIKey) != "" {
		providers = append(providers, openaiadapter.New(client, cfg.OpenAIAPIKey, openaiadapter.DefaultBaseURL, cfg.StreamIdleTimeout))
	}
	if strings.TrimSpace(cfg.AnthropicAPIKey) != "" {
		providers = append(providers, anthropic.New(client, cfg.AnthropicAPIKey, anthropic.DefaultBaseURL, cfg.StreamIdleTimeout))
	}
	registry := provider.NewRegistry(providers...)
	resolver := model.New(map[string]map[string]string{
		model.General: {
			openaiadapter.Name: cfg.GeneralOpenAIModel,
			anthropic.Name:     cfg.GeneralAnthropicModel,
			mock.Name:          "mock-model",
		},
	})
	circuitConfig := gateway.CircuitConfig{
		FailureThreshold: cfg.CircuitFailureThreshold,
		OpenDuration:     cfg.CircuitOpenDuration,
	}

	if cfg.Provider != config.ProviderAuto {
		selected, ok := registry.Get(cfg.Provider)
		if !ok {
			return nil, fmt.Errorf("configured provider is unavailable")
		}
		service := gateway.NewWithCircuitBreaker(selected, resolver, circuitConfig)
		service.SetPricing(configuredPrices(cfg))
		return service, nil
	}

	orderedNames := []string{openaiadapter.Name, anthropic.Name}
	ordered := make([]provider.Provider, 0, len(orderedNames))
	for _, name := range orderedNames {
		if item, ok := registry.Get(name); ok {
			ordered = append(ordered, item)
		}
	}
	if len(ordered) == 0 {
		return nil, fmt.Errorf("no automatic providers are configured")
	}
	service, err := gateway.NewAutoWithRouting(resolver, circuitConfig, gateway.RoutingConfig{
		Policy:                       cfg.RoutingPolicy,
		MinSamples:                   cfg.RoutingMinSamples,
		SampleMaxAge:                 cfg.RoutingSampleMaxAge,
		ExplorationInterval:          cfg.RoutingExplorationInterval,
		MaxLatencyOverFastestPercent: cfg.RoutingMaxLatencyOverFastestPercent,
	}, ordered...)
	if err != nil {
		return nil, err
	}
	service.SetPricing(configuredPrices(cfg))
	return service, nil
}

func configuredPrices(cfg config.Config) accounting.PriceBook {
	prices := accounting.PriceBook{
		{Provider: mock.Name, Model: "mock-model"}: cfg.MockPricing,
	}
	if cfg.GeneralOpenAIModel != "" {
		prices[accounting.Key{Provider: openaiadapter.Name, Model: cfg.GeneralOpenAIModel}] = cfg.OpenAIPricing
	}
	if cfg.GeneralAnthropicModel != "" {
		prices[accounting.Key{Provider: anthropic.Name, Model: cfg.GeneralAnthropicModel}] = cfg.AnthropicPricing
	}
	return prices
}
