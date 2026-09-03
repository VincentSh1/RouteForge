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
		"ROUTEFORGE_STREAM_IDLE_TIMEOUT",
		"ROUTEFORGE_CIRCUIT_FAILURE_THRESHOLD",
		"ROUTEFORGE_CIRCUIT_OPEN_DURATION",
		"ROUTEFORGE_ROUTING_MIN_SAMPLES",
		"ROUTEFORGE_ROUTING_SAMPLE_MAX_AGE",
		"ROUTEFORGE_ROUTING_EXPLORATION_INTERVAL",
		"ROUTEFORGE_ROUTING_MAX_LATENCY_OVER_FASTEST_PERCENT",
		"ROUTEFORGE_OTEL_ENABLED",
		"ROUTEFORGE_OTEL_EXPORTER_OTLP_ENDPOINT",
		"ROUTEFORGE_METRICS_ENABLED",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("ROUTEFORGE_ADDR", "127.0.0.1:8080")
	t.Setenv("ROUTEFORGE_METRICS_ADDR", defaultMetricsAddr)

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
	if cfg.Provider != ProviderMock || cfg.ProviderTimeout != 30*time.Second || cfg.StreamIdleTimeout != 30*time.Second {
		t.Fatal("unexpected provider defaults")
	}
	if cfg.CircuitFailureThreshold != 3 || cfg.CircuitOpenDuration != 30*time.Second {
		t.Fatal("unexpected circuit breaker defaults")
	}
	if cfg.RoutingPolicy != RoutingPolicyDeterministic || cfg.RoutingMinSamples != 5 || cfg.RoutingSampleMaxAge != 5*time.Minute || cfg.RoutingExplorationInterval != 10 {
		t.Fatal("unexpected routing defaults")
	}
	if cfg.OTelEnabled || cfg.OTelExporterOTLPEndpoint != "" {
		t.Fatal("OpenTelemetry must be disabled by default")
	}
	if cfg.MetricsEnabled || cfg.MetricsAddr != "127.0.0.1:9090" {
		t.Fatal("metrics must be disabled with a loopback default address")
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
	t.Setenv("ROUTEFORGE_STREAM_IDLE_TIMEOUT", "7s")
	t.Setenv("ROUTEFORGE_CIRCUIT_FAILURE_THRESHOLD", "4")
	t.Setenv("ROUTEFORGE_CIRCUIT_OPEN_DURATION", "8s")
	t.Setenv("ROUTEFORGE_ROUTING_POLICY", RoutingPolicyLatency)
	t.Setenv("ROUTEFORGE_ROUTING_MIN_SAMPLES", "7")
	t.Setenv("ROUTEFORGE_ROUTING_SAMPLE_MAX_AGE", "9m")
	t.Setenv("ROUTEFORGE_ROUTING_EXPLORATION_INTERVAL", "11")
	t.Setenv("ROUTEFORGE_OTEL_ENABLED", "true")
	t.Setenv("ROUTEFORGE_OTEL_EXPORTER_OTLP_ENDPOINT", "https://collector.example/v1/traces")
	t.Setenv("ROUTEFORGE_METRICS_ENABLED", "true")
	t.Setenv("ROUTEFORGE_METRICS_ADDR", "127.0.0.1:9191")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Addr != "127.0.0.1:9090" || cfg.ReadTimeout != 2*time.Second || cfg.ShutdownTimeout != 5*time.Second || cfg.ProviderTimeout != 6*time.Second || cfg.StreamIdleTimeout != 7*time.Second {
		t.Fatal("unexpected configuration")
	}
	if cfg.CircuitFailureThreshold != 4 || cfg.CircuitOpenDuration != 8*time.Second {
		t.Fatal("unexpected circuit breaker configuration")
	}
	if cfg.RoutingPolicy != RoutingPolicyLatency || cfg.RoutingMinSamples != 7 || cfg.RoutingSampleMaxAge != 9*time.Minute || cfg.RoutingExplorationInterval != 11 {
		t.Fatal("unexpected routing configuration")
	}
	if !cfg.OTelEnabled || cfg.OTelExporterOTLPEndpoint != "https://collector.example/v1/traces" {
		t.Fatal("unexpected OpenTelemetry configuration")
	}
	if !cfg.MetricsEnabled || cfg.MetricsAddr != "127.0.0.1:9191" {
		t.Fatal("unexpected metrics configuration")
	}
}

func TestLoadMetricsConfiguration(t *testing.T) {
	t.Run("enabled independently of tracing", func(t *testing.T) {
		setProviderDefaults(t)
		t.Setenv("ROUTEFORGE_METRICS_ENABLED", "true")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if !cfg.MetricsEnabled || cfg.OTelEnabled || cfg.MetricsAddr != defaultMetricsAddr {
			t.Fatalf("configuration = %+v", cfg)
		}
	})

	for _, address := range []string{"", "127.0.0.1", "127.0.0.1:0", "127.0.0.1:too-high"} {
		t.Run("invalid address "+address, func(t *testing.T) {
			setProviderDefaults(t)
			t.Setenv("ROUTEFORGE_METRICS_ENABLED", "true")
			t.Setenv("ROUTEFORGE_METRICS_ADDR", address)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() error = %v", err)
			}
		})
	}

	t.Run("invalid enabled value", func(t *testing.T) {
		setProviderDefaults(t)
		t.Setenv("ROUTEFORGE_METRICS_ENABLED", "sometimes")
		if _, err := Load(); err == nil {
			t.Fatal("Load() error = nil")
		}
	})

	t.Run("disabled does not require address", func(t *testing.T) {
		setProviderDefaults(t)
		t.Setenv("ROUTEFORGE_METRICS_ENABLED", "false")
		t.Setenv("ROUTEFORGE_METRICS_ADDR", "")
		if _, err := Load(); err != nil {
			t.Fatalf("Load() error = %v", err)
		}
	})
}

func TestLoadOpenTelemetryConfiguration(t *testing.T) {
	t.Run("enabled requires endpoint", func(t *testing.T) {
		setProviderDefaults(t)
		t.Setenv("ROUTEFORGE_OTEL_ENABLED", "true")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "requires ROUTEFORGE_OTEL_EXPORTER_OTLP_ENDPOINT") {
			t.Fatalf("Load() error = %v", err)
		}
	})

	for _, endpoint := range []string{
		"collector:4318",
		"ftp://collector.example/traces",
		"https://user:secret@collector.example/traces",
		"https://collector.example/traces?token=secret",
		"https://collector.example/traces#fragment",
	} {
		t.Run("invalid endpoint", func(t *testing.T) {
			setProviderDefaults(t)
			t.Setenv("ROUTEFORGE_OTEL_ENABLED", "true")
			t.Setenv("ROUTEFORGE_OTEL_EXPORTER_OTLP_ENDPOINT", endpoint)
			if _, err := Load(); err == nil || strings.Contains(err.Error(), endpoint) {
				t.Fatalf("Load() error = %v", err)
			}
		})
	}

	t.Run("invalid enabled value", func(t *testing.T) {
		setProviderDefaults(t)
		t.Setenv("ROUTEFORGE_OTEL_ENABLED", "sometimes")
		if _, err := Load(); err == nil {
			t.Fatal("Load() error = nil")
		}
	})

	t.Run("disabled ignores exporter endpoint", func(t *testing.T) {
		setProviderDefaults(t)
		t.Setenv("ROUTEFORGE_OTEL_ENABLED", "false")
		t.Setenv("ROUTEFORGE_OTEL_EXPORTER_OTLP_ENDPOINT", "not-a-url")
		if _, err := Load(); err != nil {
			t.Fatalf("Load() error = %v", err)
		}
	})
}

func TestLoadRejectsInvalidRoutingConfiguration(t *testing.T) {
	for _, test := range []struct {
		key   string
		value string
	}{
		{key: "ROUTEFORGE_ROUTING_POLICY", value: "fastest"},
		{key: "ROUTEFORGE_ROUTING_MIN_SAMPLES", value: "0"},
		{key: "ROUTEFORGE_ROUTING_MIN_SAMPLES", value: "many"},
		{key: "ROUTEFORGE_ROUTING_SAMPLE_MAX_AGE", value: "0s"},
		{key: "ROUTEFORGE_ROUTING_SAMPLE_MAX_AGE", value: "forever"},
		{key: "ROUTEFORGE_ROUTING_EXPLORATION_INTERVAL", value: "0"},
		{key: "ROUTEFORGE_ROUTING_EXPLORATION_INTERVAL", value: "-1"},
		{key: "ROUTEFORGE_ROUTING_EXPLORATION_INTERVAL", value: "many"},
	} {
		t.Run(test.key+"="+test.value, func(t *testing.T) {
			setProviderDefaults(t)
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatal("Load() error = nil, want an error")
			}
		})
	}
}

func TestLoadAcceptsCostRoutingPolicy(t *testing.T) {
	setProviderDefaults(t)
	t.Setenv("ROUTEFORGE_ROUTING_POLICY", RoutingPolicyCost)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.RoutingPolicy != RoutingPolicyCost {
		t.Fatalf("RoutingPolicy = %q, want %q", cfg.RoutingPolicy, RoutingPolicyCost)
	}
}

func TestLoadCostLatencyRoutingPolicy(t *testing.T) {
	t.Run("valid tolerance", func(t *testing.T) {
		setProviderDefaults(t)
		t.Setenv("ROUTEFORGE_ROUTING_POLICY", RoutingPolicyCostLatency)
		t.Setenv("ROUTEFORGE_ROUTING_MAX_LATENCY_OVER_FASTEST_PERCENT", "20")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.RoutingMaxLatencyOverFastestPercent == nil || *cfg.RoutingMaxLatencyOverFastestPercent != 20 {
			t.Fatalf("tolerance = %v", cfg.RoutingMaxLatencyOverFastestPercent)
		}
	})

	t.Run("zero tolerance", func(t *testing.T) {
		setProviderDefaults(t)
		t.Setenv("ROUTEFORGE_ROUTING_POLICY", RoutingPolicyCostLatency)
		t.Setenv("ROUTEFORGE_ROUTING_MAX_LATENCY_OVER_FASTEST_PERCENT", "0")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.RoutingMaxLatencyOverFastestPercent == nil || *cfg.RoutingMaxLatencyOverFastestPercent != 0 {
			t.Fatalf("tolerance = %v", cfg.RoutingMaxLatencyOverFastestPercent)
		}
	})

	t.Run("required tolerance", func(t *testing.T) {
		setProviderDefaults(t)
		t.Setenv("ROUTEFORGE_ROUTING_POLICY", RoutingPolicyCostLatency)
		if _, err := Load(); err == nil {
			t.Fatal("Load() error = nil")
		}
	})

	for _, value := range []string{"-1", "+1", "1.5", "many"} {
		t.Run("invalid "+value, func(t *testing.T) {
			setProviderDefaults(t)
			t.Setenv("ROUTEFORGE_ROUTING_POLICY", RoutingPolicyCostLatency)
			t.Setenv("ROUTEFORGE_ROUTING_MAX_LATENCY_OVER_FASTEST_PERCENT", value)
			if _, err := Load(); err == nil {
				t.Fatal("Load() error = nil")
			}
		})
	}
}

func TestOtherRoutingPoliciesDoNotRequireCostLatencyTolerance(t *testing.T) {
	for _, policy := range []string{RoutingPolicyDeterministic, RoutingPolicyLatency, RoutingPolicyCost} {
		t.Run(policy, func(t *testing.T) {
			setProviderDefaults(t)
			t.Setenv("ROUTEFORGE_ROUTING_POLICY", policy)
			if _, err := Load(); err != nil {
				t.Fatalf("Load() error = %v", err)
			}
		})
	}
}

func TestLoadPricing(t *testing.T) {
	setProviderDefaults(t)
	t.Setenv("ROUTEFORGE_PRICE_OPENAI_INPUT_USD_PER_MILLION", "1.25")
	t.Setenv("ROUTEFORGE_PRICE_OPENAI_OUTPUT_USD_PER_MILLION", "2.5")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.OpenAIPricing.InputMicroUSDPerMillion == nil || *cfg.OpenAIPricing.InputMicroUSDPerMillion != 1_250_000 ||
		cfg.OpenAIPricing.OutputMicroUSDPerMillion == nil || *cfg.OpenAIPricing.OutputMicroUSDPerMillion != 2_500_000 {
		t.Fatalf("OpenAI pricing = %+v", cfg.OpenAIPricing)
	}
	if cfg.AnthropicPricing.InputMicroUSDPerMillion != nil || cfg.MockPricing.OutputMicroUSDPerMillion != nil {
		t.Fatal("missing pricing was fabricated")
	}
}

func TestLoadRejectsInvalidPricing(t *testing.T) {
	for _, value := range []string{"-1", "1.0000001", "not-a-price"} {
		t.Run(value, func(t *testing.T) {
			setProviderDefaults(t)
			t.Setenv("ROUTEFORGE_PRICE_ANTHROPIC_INPUT_USD_PER_MILLION", value)
			if _, err := Load(); err == nil {
				t.Fatal("Load() error = nil")
			}
		})
	}
}

func TestLoadRejectsInvalidDuration(t *testing.T) {
	setProviderDefaults(t)
	t.Setenv("ROUTEFORGE_READ_TIMEOUT", "never")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want an error")
	}
}

func TestLoadRejectsInvalidStreamIdleTimeout(t *testing.T) {
	for _, value := range []string{"0s", "never"} {
		t.Run(value, func(t *testing.T) {
			setProviderDefaults(t)
			t.Setenv("ROUTEFORGE_STREAM_IDLE_TIMEOUT", value)
			if _, err := Load(); err == nil {
				t.Fatal("Load() error = nil, want an error")
			}
		})
	}
}

func TestLoadRejectsInvalidCircuitBreakerConfiguration(t *testing.T) {
	for _, test := range []struct {
		key   string
		value string
	}{
		{key: "ROUTEFORGE_CIRCUIT_FAILURE_THRESHOLD", value: "0"},
		{key: "ROUTEFORGE_CIRCUIT_FAILURE_THRESHOLD", value: "many"},
		{key: "ROUTEFORGE_CIRCUIT_OPEN_DURATION", value: "0s"},
		{key: "ROUTEFORGE_CIRCUIT_OPEN_DURATION", value: "later"},
	} {
		t.Run(test.key+"="+test.value, func(t *testing.T) {
			setProviderDefaults(t)
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatal("Load() error = nil, want an error")
			}
		})
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
	t.Setenv("ROUTEFORGE_CIRCUIT_FAILURE_THRESHOLD", "")
	t.Setenv("ROUTEFORGE_CIRCUIT_OPEN_DURATION", "")
	t.Setenv("ROUTEFORGE_ROUTING_POLICY", RoutingPolicyDeterministic)
	t.Setenv("ROUTEFORGE_ROUTING_MIN_SAMPLES", "")
	t.Setenv("ROUTEFORGE_ROUTING_SAMPLE_MAX_AGE", "")
	t.Setenv("ROUTEFORGE_ROUTING_EXPLORATION_INTERVAL", "")
	t.Setenv("ROUTEFORGE_ROUTING_MAX_LATENCY_OVER_FASTEST_PERCENT", "")
	t.Setenv("ROUTEFORGE_OTEL_ENABLED", "")
	t.Setenv("ROUTEFORGE_OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("ROUTEFORGE_METRICS_ENABLED", "")
	t.Setenv("ROUTEFORGE_METRICS_ADDR", defaultMetricsAddr)
	for _, providerName := range []string{"OPENAI", "ANTHROPIC", "MOCK"} {
		t.Setenv("ROUTEFORGE_PRICE_"+providerName+"_INPUT_USD_PER_MILLION", "")
		t.Setenv("ROUTEFORGE_PRICE_"+providerName+"_OUTPUT_USD_PER_MILLION", "")
	}
}
