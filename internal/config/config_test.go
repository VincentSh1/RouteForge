package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	setProviderDefaults(t)
	for _, key := range []string{
		"ROUTEFORGE_ADDR",
		"ROUTEFORGE_READ_TIMEOUT",
		"ROUTEFORGE_WRITE_TIMEOUT",
		"ROUTEFORGE_IDLE_TIMEOUT",
		"ROUTEFORGE_SHUTDOWN_TIMEOUT",
		"ROUTEFORGE_PROVIDER_TIMEOUT",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("ROUTEFORGE_ADDR", "127.0.0.1:8080")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Addr != "127.0.0.1:8080" {
		t.Fatalf("Addr = %q, want 127.0.0.1:8080", cfg.Addr)
	}
	if cfg.ReadTimeout != 15*time.Second || cfg.WriteTimeout != 30*time.Second {
		t.Fatal("unexpected default timeouts")
	}
	if cfg.Provider != ProviderMock || cfg.ProviderTimeout != 30*time.Second {
		t.Fatal("unexpected provider defaults")
	}
}

func TestLoadOverrides(t *testing.T) {
	setProviderDefaults(t)
	t.Setenv("ROUTEFORGE_ADDR", "127.0.0.1:9090")
	t.Setenv("ROUTEFORGE_READ_TIMEOUT", "2s")
	t.Setenv("ROUTEFORGE_WRITE_TIMEOUT", "3s")
	t.Setenv("ROUTEFORGE_IDLE_TIMEOUT", "4s")
	t.Setenv("ROUTEFORGE_SHUTDOWN_TIMEOUT", "5s")
	t.Setenv("ROUTEFORGE_PROVIDER_TIMEOUT", "6s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Addr != "127.0.0.1:9090" || cfg.ReadTimeout != 2*time.Second || cfg.ShutdownTimeout != 5*time.Second || cfg.ProviderTimeout != 6*time.Second {
		t.Fatal("unexpected configuration")
	}
}

func TestLoadRejectsInvalidDuration(t *testing.T) {
	setProviderDefaults(t)
	t.Setenv("ROUTEFORGE_READ_TIMEOUT", "never")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want an error")
	}
}

func TestLoadProviderSelection(t *testing.T) {
	tests := []struct {
		name           string
		provider       string
		openAIKey      string
		anthropicKey   string
		openAIModel    string
		anthropicModel string
		wantError      bool
	}{
		{name: "mock", provider: ProviderMock},
		{name: "OpenAI", provider: "OPENAI", openAIKey: "test-key"},
		{name: "Anthropic", provider: ProviderAnthropic, anthropicKey: "test-key"},
		{name: "auto", provider: ProviderAuto, openAIKey: "test-key", openAIModel: "openai-model"},
		{name: "auto with both providers", provider: ProviderAuto, openAIKey: "test-key", anthropicKey: "test-key", openAIModel: "openai-model", anthropicModel: "anthropic-model"},
		{name: "missing OpenAI key", provider: ProviderOpenAI, wantError: true},
		{name: "missing Anthropic key", provider: ProviderAnthropic, wantError: true},
		{name: "empty auto", provider: ProviderAuto, wantError: true},
		{name: "auto missing OpenAI mapping", provider: ProviderAuto, openAIKey: "test-key", wantError: true},
		{name: "auto missing Anthropic mapping", provider: ProviderAuto, anthropicKey: "test-key", wantError: true},
		{name: "unknown", provider: "other", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("ROUTEFORGE_PROVIDER", test.provider)
			t.Setenv("OPENAI_API_KEY", test.openAIKey)
			t.Setenv("ANTHROPIC_API_KEY", test.anthropicKey)
			t.Setenv("ROUTEFORGE_MODEL_GENERAL_OPENAI", test.openAIModel)
			t.Setenv("ROUTEFORGE_MODEL_GENERAL_ANTHROPIC", test.anthropicModel)
			cfg, err := Load()
			if test.wantError && err == nil {
				t.Fatal("Load() error = nil")
			}
			if !test.wantError && err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if !test.wantError && cfg.Provider != strings.ToLower(test.provider) {
				t.Fatalf("Provider = %q", cfg.Provider)
			}
			if !test.wantError && (cfg.GeneralOpenAIModel != test.openAIModel || cfg.GeneralAnthropicModel != test.anthropicModel) {
				t.Fatal("model mappings were not loaded")
			}
		})
	}
}

func setProviderDefaults(t *testing.T) {
	t.Helper()
	t.Setenv("ROUTEFORGE_PROVIDER", ProviderMock)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ROUTEFORGE_MODEL_GENERAL_OPENAI", "")
	t.Setenv("ROUTEFORGE_MODEL_GENERAL_ANTHROPIC", "")
}
