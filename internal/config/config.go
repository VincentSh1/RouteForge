package config

import (
	"fmt"
	"os"
	"time"
)

const (
	defaultAddr            = ":8080"
	defaultReadTimeout     = 15 * time.Second
	defaultWriteTimeout    = 30 * time.Second
	defaultIdleTimeout     = 60 * time.Second
	defaultShutdownTimeout = 10 * time.Second
)

type Config struct {
	Addr            string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		Addr:            envOrDefault("ROUTEFORGE_ADDR", defaultAddr),
		ReadTimeout:     defaultReadTimeout,
		WriteTimeout:    defaultWriteTimeout,
		IdleTimeout:     defaultIdleTimeout,
		ShutdownTimeout: defaultShutdownTimeout,
	}

	values := []struct {
		key    string
		target *time.Duration
	}{
		{"ROUTEFORGE_READ_TIMEOUT", &cfg.ReadTimeout},
		{"ROUTEFORGE_WRITE_TIMEOUT", &cfg.WriteTimeout},
		{"ROUTEFORGE_IDLE_TIMEOUT", &cfg.IdleTimeout},
		{"ROUTEFORGE_SHUTDOWN_TIMEOUT", &cfg.ShutdownTimeout},
	}
	for _, value := range values {
		raw := os.Getenv(value.key)
		if raw == "" {
			continue
		}
		duration, err := time.ParseDuration(raw)
		if err != nil || duration <= 0 {
			return Config{}, fmt.Errorf("%s must be a positive duration", value.key)
		}
		*value.target = duration
	}

	if cfg.Addr == "" {
		return Config{}, fmt.Errorf("ROUTEFORGE_ADDR must not be empty")
	}
	return cfg, nil
}

func envOrDefault(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}
