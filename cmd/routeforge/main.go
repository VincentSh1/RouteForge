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
		return gateway.NewWithCircuitBreaker(selected, resolver, circuitConfig), nil
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
	return gateway.NewAutoWithRouting(resolver, circuitConfig, gateway.RoutingConfig{
		Policy:       cfg.RoutingPolicy,
		MinSamples:   cfg.RoutingMinSamples,
		SampleMaxAge: cfg.RoutingSampleMaxAge,
	}, ordered...)
}
