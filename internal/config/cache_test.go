package config

import (
	"testing"
	"time"
)

func TestCacheConfiguration(t *testing.T) {
	tests := []struct {
		name, enabled, url, ttl string
		invalid                 bool
	}{
		{name: "disabled"}, {name: "enabled", enabled: "true", url: "redis://localhost:6379"},
		{name: "custom TTL", enabled: "true", url: "rediss://localhost:6379/0", ttl: "2m"},
		{name: "missing URL", enabled: "true", invalid: true},
		{name: "bad URL", enabled: "true", url: "invalid", invalid: true},
		{name: "bad enabled", enabled: "yes", invalid: true},
		{name: "bad TTL", ttl: "bad", invalid: true}, {name: "negative TTL", ttl: "-1s", invalid: true},
		{name: "tiny TTL", ttl: "1ns", invalid: true}, {name: "zero TTL", ttl: "0", invalid: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setProviderDefaults(t)
			t.Setenv("ROUTEFORGE_CACHE_ENABLED", test.enabled)
			t.Setenv("ROUTEFORGE_REDIS_URL", test.url)
			t.Setenv("ROUTEFORGE_CACHE_TTL", test.ttl)
			cfg, err := Load()
			if (err != nil) != test.invalid {
				t.Fatalf("unexpected config validity: %v", err)
			}
			if err == nil && test.ttl == "" && cfg.CacheTTL != 5*time.Minute {
				t.Fatal("wrong default TTL")
			}
		})
	}
}
