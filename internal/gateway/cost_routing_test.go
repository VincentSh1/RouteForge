package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/VincentSh1/RouteForge/internal/accounting"
	"github.com/VincentSh1/RouteForge/internal/model"
	"github.com/VincentSh1/RouteForge/internal/openai"
	"github.com/VincentSh1/RouteForge/internal/provider"
)

func TestCostRoutingUsesStrictPriceDominance(t *testing.T) {
	providers := routingProviders()
	policy := costRoutingPolicy{}

	tests := []struct {
		name         string
		secondInput  uint64
		secondOutput uint64
		wantFirst    string
	}{
		{name: "cheaper input and output", secondInput: 9, secondOutput: 9, wantFirst: "second"},
		{name: "equal input and cheaper output", secondInput: 10, secondOutput: 9, wantFirst: "second"},
		{name: "cheaper input and equal output", secondInput: 9, secondOutput: 10, wantFirst: "second"},
		{name: "identical prices", secondInput: 10, secondOutput: 10, wantFirst: "first"},
		{name: "cheaper input but dearer output", secondInput: 9, secondOutput: 11, wantFirst: "first"},
		{name: "dearer input but cheaper output", secondInput: 11, secondOutput: 9, wantFirst: "first"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prices := map[string]accounting.Rates{
				"first":  costRates(10, 10),
				"second": costRates(test.secondInput, test.secondOutput),
			}
			ordered := policy.order(providers, nonStreamingMode, nil, prices, time.Time{})
			if ordered[0].Name() != test.wantFirst {
				t.Fatalf("first provider = %q, want %q", ordered[0].Name(), test.wantFirst)
			}
		})
	}
}

func TestCostRoutingPreservesOrderWithMissingPricing(t *testing.T) {
	providers := routingProviders()
	policy := costRoutingPolicy{}
	partial := costRates(1, 1)
	partial.OutputMicroUSDPerMillion = nil

	for name, prices := range map[string]map[string]accounting.Rates{
		"no pricing":                  nil,
		"missing alternate":           {"first": costRates(10, 10)},
		"missing deterministic first": {"second": costRates(1, 1)},
		"partial alternate":           {"first": costRates(10, 10), "second": partial},
	} {
		t.Run(name, func(t *testing.T) {
			assertProviderOrder(t, policy.order(providers, nonStreamingMode, nil, prices, time.Time{}), "first", "second")
		})
	}
}

func TestCostRoutingMultipleProvidersUsesStablePartialOrder(t *testing.T) {
	providers := []provider.Provider{
		&recordingProvider{name: "first"},
		&recordingProvider{name: "second"},
		&recordingProvider{name: "third"},
	}
	policy := costRoutingPolicy{}

	t.Run("dominator moves ahead and ambiguity stays stable", func(t *testing.T) {
		prices := map[string]accounting.Rates{
			"first":  costRates(10, 10),
			"second": costRates(9, 12),
			"third":  costRates(8, 9),
		}
		assertProviderOrder(t, policy.order(providers, nonStreamingMode, nil, prices, time.Time{}), "third", "first", "second")
	})

	t.Run("non-dominated configured order breaks partial-order ties", func(t *testing.T) {
		prices := map[string]accounting.Rates{
			"first":  costRates(10, 10),
			"second": costRates(8, 12),
			"third":  costRates(9, 9),
		}
		assertProviderOrder(t, policy.order(providers, nonStreamingMode, nil, prices, time.Time{}), "second", "third", "first")
	})

	t.Run("dominated fallback moves behind its dominator", func(t *testing.T) {
		prices := map[string]accounting.Rates{
			"first":  costRates(10, 10),
			"second": costRates(20, 20),
			"third":  costRates(15, 15),
		}
		assertProviderOrder(t, policy.order(providers, nonStreamingMode, nil, prices, time.Time{}), "first", "third", "second")
	})
}

func TestCostRoutingResolvesNativeModelsWithoutMutatingRequest(t *testing.T) {
	first, second := &recordingProvider{name: "first"}, &recordingProvider{name: "second"}
	service := newCostTestService(t, first, second)
	service.SetPricing(accounting.PriceBook{
		{Provider: "first", Model: "first-model"}:   costRates(10, 10),
		{Provider: "second", Model: "second-model"}: costRates(1, 1),
	})
	req := validRequest()

	response, err := service.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if response.ID != "second" || second.request.Model != "second-model" || first.calls != 0 {
		t.Fatalf("response=%+v first calls=%d second request=%+v", response, first.calls, second.request)
	}
	if req.Model != model.General {
		t.Fatalf("original request model = %q", req.Model)
	}
}

func TestCostRoutingPreservesExplicitProviderSelection(t *testing.T) {
	selected := &recordingProvider{name: "first"}
	service := New(selected, testResolver())
	service.SetPricing(accounting.PriceBook{
		{Provider: "first", Model: "provider-native-model"}: costRates(100, 100),
		{Provider: "second", Model: "second-model"}:         costRates(1, 1),
	})
	req := openai.ChatCompletionRequest{Model: "provider-native-model", Messages: []openai.Message{{Role: "user"}}}

	if _, err := service.Complete(context.Background(), req); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if selected.calls != 1 || selected.request.Model != req.Model {
		t.Fatalf("selected provider = %+v", selected)
	}
}

func TestCostRoutingHonorsCircuitEligibilityAndHalfOpenAdmission(t *testing.T) {
	clock := newManualClock()
	first, second := &recordingProvider{name: "first"}, &recordingProvider{name: "second"}
	service := newCostTestService(t, first, second)
	service.health = newHealthTracker([]string{"first", "second"}, CircuitConfig{FailureThreshold: 1, OpenDuration: time.Minute}, clock.Now)
	service.now = clock.Now
	service.SetPricing(accounting.PriceBook{
		{Provider: "first", Model: "first-model"}:   costRates(1, 1),
		{Provider: "second", Model: "second-model"}: costRates(10, 10),
	})
	attempt, _ := service.health.begin("first")
	attempt.failure()

	response, err := service.Complete(context.Background(), validRequest())
	if err != nil || response.ID != "second" || first.calls != 0 {
		t.Fatalf("open circuit response=%+v error=%v calls=%d,%d", response, err, first.calls, second.calls)
	}

	clock.Advance(time.Minute)
	response, err = service.Complete(context.Background(), validRequest())
	if err != nil || response.ID != "first" || first.calls != 1 {
		t.Fatalf("half-open response=%+v error=%v calls=%d,%d", response, err, first.calls, second.calls)
	}
	snapshot, _ := service.health.snapshot("first")
	if snapshot.State != circuitClosed {
		t.Fatalf("circuit state = %q", snapshot.State)
	}
}

func TestCostOrderedFallbackRecordsOnlyActualAttempts(t *testing.T) {
	first := &recordingProvider{name: "first"}
	second := &recordingProvider{name: "second", err: provider.NewError(provider.ErrorUnavailable, "second", errors.New("unavailable"))}
	service := newCostTestService(t, first, second)
	service.SetPricing(accounting.PriceBook{
		{Provider: "first", Model: "first-model"}:   costRates(10, 10),
		{Provider: "second", Model: "second-model"}: costRates(1, 1),
	})

	response, err := service.Complete(context.Background(), validRequest())
	if err != nil || response.ID != "first" || first.calls != 1 || second.calls != 1 {
		t.Fatalf("response=%+v error=%v calls=%d,%d", response, err, first.calls, second.calls)
	}
	firstTelemetry, _ := service.TelemetrySnapshot("first")
	secondTelemetry, _ := service.TelemetrySnapshot("second")
	if firstTelemetry.Attempts != 1 || secondTelemetry.Attempts != 1 || len(service.AccountingSnapshot().Models) != 2 {
		t.Fatalf("telemetry=%+v,%+v accounting=%+v", firstTelemetry, secondTelemetry, service.AccountingSnapshot())
	}
}

func TestMissingPricingDoesNotExcludeFallback(t *testing.T) {
	first := &recordingProvider{name: "first", err: provider.NewError(provider.ErrorUnavailable, "first", errors.New("unavailable"))}
	second := &recordingProvider{name: "second"}
	service := newCostTestService(t, first, second)
	service.SetPricing(accounting.PriceBook{{Provider: "second", Model: "second-model"}: costRates(1, 1)})

	response, err := service.Complete(context.Background(), validRequest())
	if err != nil || response.ID != "second" || first.calls != 1 || second.calls != 1 {
		t.Fatalf("response=%+v error=%v calls=%d,%d", response, err, first.calls, second.calls)
	}
}

func TestCostRoutingStreamingUsesStableFallbackAndCommitment(t *testing.T) {
	t.Run("pre-commit failure falls back", func(t *testing.T) {
		first := &streamingTestProvider{name: "first", chunks: []provider.StreamChunk{{Content: "fallback"}}}
		second := &streamingTestProvider{name: "second", err: provider.NewError(provider.ErrorTimeout, "second", errors.New("timeout"))}
		service := newCostTestService(t, first, second)
		service.SetPricing(accounting.PriceBook{
			{Provider: "first", Model: "first-model"}:   costRates(10, 10),
			{Provider: "second", Model: "second-model"}: costRates(1, 1),
		})

		if err := service.Stream(context.Background(), streamRequest(), func(provider.StreamChunk) error { return nil }); err != nil {
			t.Fatalf("Stream() error = %v", err)
		}
		if first.streamCalls != 1 || second.streamCalls != 1 {
			t.Fatalf("stream calls = %d,%d", first.streamCalls, second.streamCalls)
		}
	})

	t.Run("post-commit failure does not fall back", func(t *testing.T) {
		first := &streamingTestProvider{name: "first", chunks: []provider.StreamChunk{{Content: "unused"}}}
		second := &streamingTestProvider{
			name: "second", chunks: []provider.StreamChunk{{Content: "partial"}}, errAfter: 1,
			err: provider.NewError(provider.ErrorTimeout, "second", errors.New("timeout")),
		}
		service := newCostTestService(t, first, second)
		service.SetPricing(accounting.PriceBook{
			{Provider: "first", Model: "first-model"}:   costRates(10, 10),
			{Provider: "second", Model: "second-model"}: costRates(1, 1),
		})

		err := service.Stream(context.Background(), streamRequest(), func(provider.StreamChunk) error { return nil })
		if err == nil || first.streamCalls != 0 || second.streamCalls != 1 {
			t.Fatalf("error=%v stream calls=%d,%d", err, first.streamCalls, second.streamCalls)
		}
	})
}

func TestCostRoutingIgnoresHistoricalAccountingAndOtherPoliciesIgnorePrices(t *testing.T) {
	t.Run("cost ignores accounting totals", func(t *testing.T) {
		first, second := &recordingProvider{name: "first"}, &recordingProvider{name: "second"}
		service := newCostTestService(t, first, second)
		service.SetPricing(accounting.PriceBook{
			{Provider: "first", Model: "first-model"}:   costRates(10, 10),
			{Provider: "second", Model: "second-model"}: costRates(1, 1),
		})
		service.accounting.Record("second", "second-model", openai.NewUsage(1_000_000, 1_000_000, 2_000_000))
		service.accounting.Record("first", "first-model", openai.NewUsage(1, 1, 2))

		response, err := service.Complete(context.Background(), validRequest())
		if err != nil || response.ID != "second" {
			t.Fatalf("response=%+v error=%v", response, err)
		}
	})

	t.Run("latency ignores prices", func(t *testing.T) {
		clock := newManualClock()
		first, second := &recordingProvider{name: "first"}, &recordingProvider{name: "second"}
		service := newLatencyTestService(t, clock, first, second)
		seedRoutingTelemetry(service, "first", nonStreamingMode, clock.Now(), 10*time.Millisecond, 5)
		seedRoutingTelemetry(service, "second", nonStreamingMode, clock.Now(), 100*time.Millisecond, 5)
		service.SetPricing(accounting.PriceBook{
			{Provider: "first", Model: "first-model"}:   costRates(100, 100),
			{Provider: "second", Model: "second-model"}: costRates(1, 1),
		})

		response, err := service.Complete(context.Background(), validRequest())
		if err != nil || response.ID != "first" {
			t.Fatalf("response=%+v error=%v", response, err)
		}
	})
}

func newCostTestService(t *testing.T, providers ...provider.Provider) *Service {
	t.Helper()
	service, err := NewAutoWithRouting(testResolver(), CircuitConfig{FailureThreshold: 1, OpenDuration: time.Minute}, RoutingConfig{
		Policy: RoutingPolicyCost,
	}, providers...)
	if err != nil {
		t.Fatalf("NewAutoWithRouting() error = %v", err)
	}
	return service
}

func costRates(input, output uint64) accounting.Rates {
	return accounting.Rates{InputMicroUSDPerMillion: &input, OutputMicroUSDPerMillion: &output}
}
