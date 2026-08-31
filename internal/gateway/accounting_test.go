package gateway

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/VincentSh1/RouteForge/internal/accounting"
	"github.com/VincentSh1/RouteForge/internal/openai"
	"github.com/VincentSh1/RouteForge/internal/provider"
)

func TestFallbackAccountsEachResolvedProviderModelIndependently(t *testing.T) {
	first := &usageCompletionProvider{
		name: "first", usage: openai.NewUsage(5, 2, 7),
		err: provider.NewError(provider.ErrorUnavailable, "first", errors.New("unavailable")),
	}
	second := &usageCompletionProvider{name: "second", usage: openai.NewUsage(5, 4, 9)}
	service := NewAuto(testResolver(), first, second)
	inputRate, outputRate := uint64(1_000_000), uint64(2_000_000)
	service.SetPricing(accounting.PriceBook{
		{Provider: "first", Model: "first-model"}:   {InputMicroUSDPerMillion: &inputRate, OutputMicroUSDPerMillion: &outputRate},
		{Provider: "second", Model: "second-model"}: {InputMicroUSDPerMillion: &inputRate, OutputMicroUSDPerMillion: &outputRate},
	})

	if _, err := service.Complete(context.Background(), validRequest()); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	snapshot := service.AccountingSnapshot()
	firstModel := accountingModel(t, snapshot, "first", "first-model")
	secondModel := accountingModel(t, snapshot, "second", "second-model")
	if firstModel.Attempts != 1 || firstModel.InputTokens != 5 || firstModel.OutputTokens != 2 ||
		secondModel.Attempts != 1 || secondModel.InputTokens != 5 || secondModel.OutputTokens != 4 {
		t.Fatalf("accounting = first:%+v second:%+v", firstModel, secondModel)
	}
}

func TestExplicitNativeModelAccountingAndMissingUsage(t *testing.T) {
	provider := &usageCompletionProvider{name: "openai"}
	service := New(provider, testResolver())
	req := openai.ChatCompletionRequest{
		Model: "native-model", Messages: []openai.Message{{Role: "user", Content: "hello"}},
	}
	if _, err := service.Complete(context.Background(), req); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	model := accountingModel(t, service.AccountingSnapshot(), "openai", "native-model")
	if model.Attempts != 1 || model.AttemptsWithoutUsage != 1 || model.AttemptsWithoutEstimatedCost != 1 {
		t.Fatalf("model accounting = %+v", model)
	}
}

func TestCircuitSkippedProviderCreatesNoAccountingEntry(t *testing.T) {
	clock := newManualClock()
	first := &usageCompletionProvider{name: "first", usage: openai.NewUsage(1, 1, 2)}
	second := &usageCompletionProvider{name: "second", usage: openai.NewUsage(2, 2, 4)}
	service := NewAutoWithCircuitBreaker(testResolver(), CircuitConfig{FailureThreshold: 1, OpenDuration: time.Minute}, first, second)
	service.health = newHealthTracker([]string{"first", "second"}, CircuitConfig{FailureThreshold: 1, OpenDuration: time.Minute}, clock.Now)
	attempt, _ := service.health.begin("first")
	attempt.failure()

	if _, err := service.Complete(context.Background(), validRequest()); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	snapshot := service.AccountingSnapshot()
	if len(snapshot.Models) != 1 || snapshot.Models[0].Provider != "second" {
		t.Fatalf("accounting snapshot = %+v", snapshot)
	}
}

func TestStreamingUsageMetadataIsInternalAndDoesNotAffectTTFC(t *testing.T) {
	clock := newManualClock()
	upstream := &timedStreamingProvider{name: "first", clock: clock, steps: []streamTelemetryStep{
		{advance: 2 * time.Millisecond, chunk: provider.StreamChunk{Usage: openai.NewUsage(3, 2, 5)}},
		{advance: 3 * time.Millisecond, chunk: provider.StreamChunk{Content: "hello"}},
		{advance: time.Millisecond, err: io.EOF},
	}}
	service := New(upstream, testResolver())
	service.telemetry = newTelemetryTracker([]string{"first"}, 4, clock.Now)
	emitted := 0
	if err := service.Stream(context.Background(), streamRequest(), func(chunk provider.StreamChunk) error {
		emitted++
		if chunk.Content != "hello" || chunk.Usage != nil {
			t.Fatalf("client-visible chunk = %+v", chunk)
		}
		return nil
	}); err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	if emitted != 1 {
		t.Fatalf("emitted chunks = %d", emitted)
	}
	telemetry, _ := service.TelemetrySnapshot("first")
	if !equalDurations(telemetry.StreamingTimeToFirstContent, []time.Duration{5 * time.Millisecond}) {
		t.Fatalf("TTFC = %v", telemetry.StreamingTimeToFirstContent)
	}
	model := accountingModel(t, service.AccountingSnapshot(), "first", "first-model")
	if model.AttemptsWithUsage != 1 || model.TotalTokens != 5 {
		t.Fatalf("model accounting = %+v", model)
	}
}

func TestStreamingUsageSurvivesFailureAndFallback(t *testing.T) {
	first := &streamingTestProvider{
		name:     "first",
		chunks:   []provider.StreamChunk{{Usage: openai.NewUsage(5, 1, 6)}},
		errAfter: 1,
		err:      provider.NewError(provider.ErrorUnavailable, "first", errors.New("unavailable")),
	}
	second := &streamingTestProvider{
		name: "second",
		chunks: []provider.StreamChunk{
			{Usage: openai.NewUsage(5, 3, 8)},
			{Content: "fallback"},
		},
	}
	service := NewAuto(testResolver(), first, second)
	if err := service.Stream(context.Background(), streamRequest(), func(provider.StreamChunk) error { return nil }); err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	firstModel := accountingModel(t, service.AccountingSnapshot(), "first", "first-model")
	secondModel := accountingModel(t, service.AccountingSnapshot(), "second", "second-model")
	if firstModel.Attempts != 1 || firstModel.TotalTokens != 6 || secondModel.Attempts != 1 || secondModel.TotalTokens != 8 {
		t.Fatalf("accounting = first:%+v second:%+v", firstModel, secondModel)
	}
}

func TestStreamingUsageSurvivesPostCommitFailure(t *testing.T) {
	upstream := &streamingTestProvider{
		name: "first",
		chunks: []provider.StreamChunk{
			{Usage: openai.NewUsage(5, 2, 7)},
			{Content: "partial"},
		},
		errAfter: 2,
		err:      provider.NewError(provider.ErrorUnavailable, "first", errors.New("unavailable")),
	}
	service := New(upstream, testResolver())
	emitted := 0
	err := service.Stream(context.Background(), streamRequest(), func(provider.StreamChunk) error {
		emitted++
		return nil
	})
	if err == nil || emitted != 1 {
		t.Fatalf("Stream() error = %v, emitted = %d", err, emitted)
	}
	model := accountingModel(t, service.AccountingSnapshot(), "first", "first-model")
	if model.AttemptsWithUsage != 1 || model.TotalTokens != 7 {
		t.Fatalf("model accounting = %+v", model)
	}
}

func TestStreamingMissingAndPartialUsageRemainHonest(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		upstream := &streamingTestProvider{name: "first", chunks: []provider.StreamChunk{{Content: "done"}}}
		service := New(upstream, testResolver())
		if err := service.Stream(context.Background(), streamRequest(), func(provider.StreamChunk) error { return nil }); err != nil {
			t.Fatalf("Stream() error = %v", err)
		}
		model := accountingModel(t, service.AccountingSnapshot(), "first", "first-model")
		if model.AttemptsWithoutUsage != 1 || model.AttemptsWithUsage != 0 {
			t.Fatalf("model accounting = %+v", model)
		}
	})

	t.Run("partial before cancellation", func(t *testing.T) {
		input := uint64(9)
		upstream := &streamingTestProvider{
			name: "first", chunks: []provider.StreamChunk{{Usage: &openai.Usage{InputTokens: &input}}, {Content: "partial"}},
		}
		service := New(upstream, testResolver())
		err := service.Stream(context.Background(), streamRequest(), func(provider.StreamChunk) error { return context.Canceled })
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Stream() error = %v", err)
		}
		model := accountingModel(t, service.AccountingSnapshot(), "first", "first-model")
		if model.AttemptsWithPartialUsage != 1 || model.InputTokens != 9 || model.AttemptsWithoutEstimatedCost != 1 {
			t.Fatalf("model accounting = %+v", model)
		}
	})
}

func TestConfiguredPricesDoNotChangeRoutingOrder(t *testing.T) {
	first := &usageCompletionProvider{name: "first", usage: openai.NewUsage(1, 1, 2)}
	second := &usageCompletionProvider{name: "second", usage: openai.NewUsage(1, 1, 2)}
	service := NewAuto(testResolver(), first, second)
	expensive, cheap := uint64(10_000_000), uint64(1)
	service.SetPricing(accounting.PriceBook{
		{Provider: "first", Model: "first-model"}:   {InputMicroUSDPerMillion: &expensive, OutputMicroUSDPerMillion: &expensive},
		{Provider: "second", Model: "second-model"}: {InputMicroUSDPerMillion: &cheap, OutputMicroUSDPerMillion: &cheap},
	})
	response, err := service.Complete(context.Background(), validRequest())
	if err != nil || response.ID != "first" || first.calls != 1 || second.calls != 0 {
		t.Fatalf("response=%+v error=%v calls=%d,%d", response, err, first.calls, second.calls)
	}
}

type usageCompletionProvider struct {
	name  string
	usage *openai.Usage
	err   error
	calls int
}

func (p *usageCompletionProvider) Name() string { return p.name }

func (p *usageCompletionProvider) Complete(_ context.Context, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	p.calls++
	return openai.ChatCompletionResponse{ID: p.name, Model: req.Model, Usage: p.usage}, p.err
}

func accountingModel(t *testing.T, snapshot accounting.Snapshot, providerName, modelName string) accounting.ModelSnapshot {
	t.Helper()
	for _, model := range snapshot.Models {
		if model.Provider == providerName && model.Model == modelName {
			return model
		}
	}
	t.Fatalf("accounting model %s/%s not found in %+v", providerName, modelName, snapshot)
	return accounting.ModelSnapshot{}
}
