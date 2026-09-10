package config

import (
	"os"
	"testing"
)

func TestAdminConfiguration(t *testing.T) {
	for _, test := range []struct {
		name, enabled, addr string
		postgres, invalid   bool
	}{
		{name: "disabled"}, {name: "enabled", enabled: "true", postgres: true}, {name: "Compose override", enabled: "true", addr: "0.0.0.0:8081", postgres: true},
		{name: "needs PostgreSQL", enabled: "true", invalid: true}, {name: "invalid bool", enabled: "maybe", invalid: true},
		{name: "bad address", enabled: "true", addr: "invalid", postgres: true, invalid: true}, {name: "implicit bind", enabled: "true", addr: ":8081", postgres: true, invalid: true},
		{name: "zero port", enabled: "true", addr: "127.0.0.1:0", postgres: true, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			setProviderDefaults(t)
			t.Setenv("ROUTEFORGE_ADMIN_ENABLED", test.enabled)
			t.Setenv("ROUTEFORGE_ADMIN_ADDR", test.addr)
			if test.addr == "" {
				if err := os.Unsetenv("ROUTEFORGE_ADMIN_ADDR"); err != nil {
					t.Fatal("could not unset admin address for default test")
				}
			}
			if test.postgres {
				t.Setenv("ROUTEFORGE_POSTGRES_ENABLED", "true")
				t.Setenv("ROUTEFORGE_DATABASE_URL", "postgres://localhost/routeforge")
			}
			cfg, err := Load()
			if (err != nil) != test.invalid {
				t.Fatalf("unexpected configuration validity: %v", err)
			}
			if err == nil && test.addr == "" && cfg.AdminAddr != "127.0.0.1:8081" {
				t.Fatal("unsafe default bind")
			}
			if err == nil && test.enabled == "" && cfg.AdminEnabled {
				t.Fatal("admin enabled by default")
			}
		})
	}
}
