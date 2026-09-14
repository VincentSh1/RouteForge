package config

import (
	"strings"
	"testing"
	"time"
)

func TestAdminAuthConfiguration(t *testing.T) {
	for _, test := range []struct {
		name, secret, ttl, origin string
		invalid                   bool
	}{
		{name: "valid", secret: strings.Repeat("x", 32)}, {name: "missing", invalid: true}, {name: "short", secret: "bad", invalid: true},
		{name: "ttl", secret: strings.Repeat("x", 32), ttl: "0s", invalid: true}, {name: "long ttl", secret: strings.Repeat("x", 32), ttl: "25h", invalid: true},
		{name: "malformed ttl", secret: strings.Repeat("x", 32), ttl: "private", invalid: true},
		{name: "origin path", secret: strings.Repeat("x", 32), origin: "http://127.0.0.1/path", invalid: true},
		{name: "remote HTTP", secret: strings.Repeat("x", 32), origin: "http://example.invalid", invalid: true},
		{name: "HTTPS", secret: strings.Repeat("x", 32), origin: "https://example.invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			setProviderDefaults(t)
			t.Setenv("ROUTEFORGE_ADMIN_ENABLED", "true")
			t.Setenv("ROUTEFORGE_POSTGRES_ENABLED", "true")
			t.Setenv("ROUTEFORGE_DATABASE_URL", "postgres://localhost/routeforge")
			t.Setenv("ROUTEFORGE_ADMIN_AUTH_ENABLED", "true")
			t.Setenv("ROUTEFORGE_ADMIN_SECRET", test.secret)
			t.Setenv("ROUTEFORGE_ADMIN_SESSION_TTL", test.ttl)
			if test.origin != "" {
				t.Setenv("ROUTEFORGE_ADMIN_ORIGIN", test.origin)
			}
			cfg, err := Load()
			if (err != nil) != test.invalid {
				t.Fatal("incorrect auth configuration validation")
			}
			if err == nil && cfg.AdminSessionTTL != 8*time.Hour {
				t.Fatal("wrong default TTL")
			}
			if err != nil && (strings.Contains(err.Error(), strings.Repeat("x", 32)) || strings.Contains(err.Error(), "private")) {
				t.Fatal("configuration leaked input")
			}
		})
	}
}
