package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	for _, key := range []string{
		"ROUTEFORGE_ADDR",
		"ROUTEFORGE_READ_TIMEOUT",
		"ROUTEFORGE_WRITE_TIMEOUT",
		"ROUTEFORGE_IDLE_TIMEOUT",
		"ROUTEFORGE_SHUTDOWN_TIMEOUT",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("ROUTEFORGE_ADDR", ":8080")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Addr != ":8080" {
		t.Fatalf("Addr = %q, want :8080", cfg.Addr)
	}
	if cfg.ReadTimeout != 15*time.Second || cfg.WriteTimeout != 30*time.Second {
		t.Fatalf("unexpected default timeouts: %+v", cfg)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("ROUTEFORGE_ADDR", "127.0.0.1:9090")
	t.Setenv("ROUTEFORGE_READ_TIMEOUT", "2s")
	t.Setenv("ROUTEFORGE_WRITE_TIMEOUT", "3s")
	t.Setenv("ROUTEFORGE_IDLE_TIMEOUT", "4s")
	t.Setenv("ROUTEFORGE_SHUTDOWN_TIMEOUT", "5s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Addr != "127.0.0.1:9090" || cfg.ReadTimeout != 2*time.Second || cfg.ShutdownTimeout != 5*time.Second {
		t.Fatalf("unexpected configuration: %+v", cfg)
	}
}

func TestLoadRejectsInvalidDuration(t *testing.T) {
	t.Setenv("ROUTEFORGE_READ_TIMEOUT", "never")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want an error")
	}
}
