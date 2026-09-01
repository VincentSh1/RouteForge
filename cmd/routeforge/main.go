package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/VincentSh1/RouteForge/internal/accounting"
	"github.com/VincentSh1/RouteForge/internal/config"
	"github.com/VincentSh1/RouteForge/internal/gateway"
	"github.com/VincentSh1/RouteForge/internal/httpapi"
	"github.com/VincentSh1/RouteForge/internal/model"
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

	service, err := buildService(cfg)
	if err != nil {
		return err
	}
	handler := httpapi.NewHandler(service)
	server := httpapi.NewServer(cfg, handler.Routes())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		slog.Info("RouteForge listening")
		serverErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	return server.Shutdown(shutdownCtx)
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
