package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/VincentSh1/RouteForge/internal/accounting"
)

const (
	defaultAddr                       = "127.0.0.1:8080"
	defaultReadTimeout                = 15 * time.Second
	defaultWriteTimeout               = 30 * time.Second
	defaultIdleTimeout                = 60 * time.Second
	defaultShutdownTimeout            = 10 * time.Second
	defaultProviderTimeout            = 30 * time.Second
	defaultStreamIdleTimeout          = 30 * time.Second
	defaultCircuitOpenDuration        = 30 * time.Second
	defaultCircuitFailureThreshold    = 3
	defaultRoutingMinSamples          = 5
	defaultRoutingSampleMaxAge        = 5 * time.Minute
	defaultRoutingExplorationInterval = 10
	defaultProvider                   = "mock"
	defaultRoutingPolicy              = RoutingPolicyDeterministic
)

const (
	ProviderMock      = "mock"
	ProviderOpenAI    = "openai"
	ProviderAnthropic = "anthropic"
	ProviderAuto      = "auto"

	RoutingPolicyDeterministic = "deterministic"
	RoutingPolicyLatency       = "latency"
	RoutingPolicyCost          = "cost"
)

type Config struct {
	Addr                       string
	ReadTimeout                time.Duration
	WriteTimeout               time.Duration
	IdleTimeout                time.Duration
	ShutdownTimeout            time.Duration
	ProviderTimeout            time.Duration
	StreamIdleTimeout          time.Duration
	CircuitFailureThreshold    int
	CircuitOpenDuration        time.Duration
	RoutingPolicy              string
	RoutingMinSamples          int
	RoutingSampleMaxAge        time.Duration
	RoutingExplorationInterval int
	Provider                   string
	OpenAIAPIKey               string
	AnthropicAPIKey            string
	GeneralOpenAIModel         string
	GeneralAnthropicModel      string
	OpenAIPricing              accounting.Rates
	AnthropicPricing           accounting.Rates
	MockPricing                accounting.Rates
}

type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

func Load() (Config, error) {
	cfg := Config{
		Addr:                       envOrDefault("ROUTEFORGE_ADDR", defaultAddr),
		ReadTimeout:                defaultReadTimeout,
		WriteTimeout:               defaultWriteTimeout,
		IdleTimeout:                defaultIdleTimeout,
		ShutdownTimeout:            defaultShutdownTimeout,
		ProviderTimeout:            defaultProviderTimeout,
		StreamIdleTimeout:          defaultStreamIdleTimeout,
		CircuitFailureThreshold:    defaultCircuitFailureThreshold,
		CircuitOpenDuration:        defaultCircuitOpenDuration,
		RoutingPolicy:              strings.ToLower(strings.TrimSpace(envOrDefault("ROUTEFORGE_ROUTING_POLICY", defaultRoutingPolicy))),
		RoutingMinSamples:          defaultRoutingMinSamples,
		RoutingSampleMaxAge:        defaultRoutingSampleMaxAge,
		RoutingExplorationInterval: defaultRoutingExplorationInterval,
		Provider:                   strings.ToLower(strings.TrimSpace(envOrDefault("ROUTEFORGE_PROVIDER", defaultProvider))),
		OpenAIAPIKey:               os.Getenv("OPENAI_API_KEY"),
		AnthropicAPIKey:            os.Getenv("ANTHROPIC_API_KEY"),
		GeneralOpenAIModel:         strings.TrimSpace(os.Getenv("ROUTEFORGE_MODEL_GENERAL_OPENAI")),
		GeneralAnthropicModel:      strings.TrimSpace(os.Getenv("ROUTEFORGE_MODEL_GENERAL_ANTHROPIC")),
	}
	var err error
	if cfg.OpenAIPricing, err = loadPricing("OPENAI"); err != nil {
		return Config{}, err
	}
	if cfg.AnthropicPricing, err = loadPricing("ANTHROPIC"); err != nil {
		return Config{}, err
	}
	if cfg.MockPricing, err = loadPricing("MOCK"); err != nil {
		return Config{}, err
	}

	values := []struct {
		key    string
		target *time.Duration
	}{
		{"ROUTEFORGE_READ_TIMEOUT", &cfg.ReadTimeout},
		{"ROUTEFORGE_WRITE_TIMEOUT", &cfg.WriteTimeout},
		{"ROUTEFORGE_IDLE_TIMEOUT", &cfg.IdleTimeout},
		{"ROUTEFORGE_SHUTDOWN_TIMEOUT", &cfg.ShutdownTimeout},
		{"ROUTEFORGE_PROVIDER_TIMEOUT", &cfg.ProviderTimeout},
		{"ROUTEFORGE_STREAM_IDLE_TIMEOUT", &cfg.StreamIdleTimeout},
		{"ROUTEFORGE_CIRCUIT_OPEN_DURATION", &cfg.CircuitOpenDuration},
		{"ROUTEFORGE_ROUTING_SAMPLE_MAX_AGE", &cfg.RoutingSampleMaxAge},
	}
	if raw := os.Getenv("ROUTEFORGE_CIRCUIT_FAILURE_THRESHOLD"); raw != "" {
		threshold, err := strconv.Atoi(raw)
		if err != nil || threshold <= 0 {
			return Config{}, validationError("ROUTEFORGE_CIRCUIT_FAILURE_THRESHOLD must be a positive integer")
		}
		cfg.CircuitFailureThreshold = threshold
	}
	if raw := os.Getenv("ROUTEFORGE_ROUTING_MIN_SAMPLES"); raw != "" {
		minimum, err := strconv.Atoi(raw)
		if err != nil || minimum <= 0 {
			return Config{}, validationError("ROUTEFORGE_ROUTING_MIN_SAMPLES must be a positive integer")
		}
		cfg.RoutingMinSamples = minimum
	}
	if raw := os.Getenv("ROUTEFORGE_ROUTING_EXPLORATION_INTERVAL"); raw != "" {
		interval, err := strconv.Atoi(raw)
		if err != nil || interval <= 0 {
			return Config{}, validationError("ROUTEFORGE_ROUTING_EXPLORATION_INTERVAL must be a positive integer")
		}
		cfg.RoutingExplorationInterval = interval
	}
	for _, value := range values {
		raw := os.Getenv(value.key)
		if raw == "" {
			continue
		}
		duration, err := time.ParseDuration(raw)
		if err != nil || duration <= 0 {
			return Config{}, validationError("%s must be a positive duration", value.key)
		}
		*value.target = duration
	}

	if cfg.Addr == "" {
		return Config{}, validationError("ROUTEFORGE_ADDR must not be empty")
	}
	switch cfg.RoutingPolicy {
	case RoutingPolicyDeterministic, RoutingPolicyLatency, RoutingPolicyCost:
	default:
		return Config{}, validationError("ROUTEFORGE_ROUTING_POLICY must be deterministic, latency, or cost")
	}
	switch cfg.Provider {
	case ProviderMock:
	case ProviderOpenAI:
		if strings.TrimSpace(cfg.OpenAIAPIKey) == "" {
			return Config{}, validationError("ROUTEFORGE_PROVIDER=openai requires OPENAI_API_KEY")
		}
	case ProviderAnthropic:
		if strings.TrimSpace(cfg.AnthropicAPIKey) == "" {
			return Config{}, validationError("ROUTEFORGE_PROVIDER=anthropic requires ANTHROPIC_API_KEY")
		}
	case ProviderAuto:
		if strings.TrimSpace(cfg.OpenAIAPIKey) == "" && strings.TrimSpace(cfg.AnthropicAPIKey) == "" {
			return Config{}, validationError("ROUTEFORGE_PROVIDER=auto requires at least one provider API key")
		}
		if strings.TrimSpace(cfg.OpenAIAPIKey) != "" && cfg.GeneralOpenAIModel == "" {
			return Config{}, validationError("auto routing with OpenAI requires ROUTEFORGE_MODEL_GENERAL_OPENAI")
		}
		if strings.TrimSpace(cfg.AnthropicAPIKey) != "" && cfg.GeneralAnthropicModel == "" {
			return Config{}, validationError("auto routing with Anthropic requires ROUTEFORGE_MODEL_GENERAL_ANTHROPIC")
		}
	default:
		return Config{}, validationError("ROUTEFORGE_PROVIDER must be mock, openai, anthropic, or auto")
	}
	return cfg, nil
}

func validationError(format string, args ...any) error {
	return &ValidationError{Message: fmt.Sprintf(format, args...)}
}

func envOrDefault(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func loadPricing(providerName string) (accounting.Rates, error) {
	input, err := loadPrice("ROUTEFORGE_PRICE_" + providerName + "_INPUT_USD_PER_MILLION")
	if err != nil {
		return accounting.Rates{}, err
	}
	output, err := loadPrice("ROUTEFORGE_PRICE_" + providerName + "_OUTPUT_USD_PER_MILLION")
	if err != nil {
		return accounting.Rates{}, err
	}
	return accounting.Rates{InputMicroUSDPerMillion: input, OutputMicroUSDPerMillion: output}, nil
}

func loadPrice(key string) (*uint64, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	price, err := accounting.ParseUSDPerMillion(raw)
	if err != nil {
		return nil, validationError("%s must be a non-negative USD amount with at most six decimal places", key)
	}
	return &price, nil
}
