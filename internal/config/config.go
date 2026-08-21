package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	defaultAddr            = "127.0.0.1:8080"
	defaultReadTimeout     = 15 * time.Second
	defaultWriteTimeout    = 30 * time.Second
	defaultIdleTimeout     = 60 * time.Second
	defaultShutdownTimeout = 10 * time.Second
	defaultProviderTimeout = 30 * time.Second
	defaultProvider        = "mock"
)

const (
	ProviderMock      = "mock"
	ProviderOpenAI    = "openai"
	ProviderAnthropic = "anthropic"
	ProviderAuto      = "auto"
)

type Config struct {
	Addr                  string
	ReadTimeout           time.Duration
	WriteTimeout          time.Duration
	IdleTimeout           time.Duration
	ShutdownTimeout       time.Duration
	ProviderTimeout       time.Duration
	Provider              string
	OpenAIAPIKey          string
	AnthropicAPIKey       string
	GeneralOpenAIModel    string
	GeneralAnthropicModel string
}

type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

func Load() (Config, error) {
	cfg := Config{
		Addr:                  envOrDefault("ROUTEFORGE_ADDR", defaultAddr),
		ReadTimeout:           defaultReadTimeout,
		WriteTimeout:          defaultWriteTimeout,
		IdleTimeout:           defaultIdleTimeout,
		ShutdownTimeout:       defaultShutdownTimeout,
		ProviderTimeout:       defaultProviderTimeout,
		Provider:              strings.ToLower(strings.TrimSpace(envOrDefault("ROUTEFORGE_PROVIDER", defaultProvider))),
		OpenAIAPIKey:          os.Getenv("OPENAI_API_KEY"),
		AnthropicAPIKey:       os.Getenv("ANTHROPIC_API_KEY"),
		GeneralOpenAIModel:    strings.TrimSpace(os.Getenv("ROUTEFORGE_MODEL_GENERAL_OPENAI")),
		GeneralAnthropicModel: strings.TrimSpace(os.Getenv("ROUTEFORGE_MODEL_GENERAL_ANTHROPIC")),
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
