package gateway

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/VincentSh1/RouteForge/internal/accounting"
	"github.com/VincentSh1/RouteForge/internal/openai"
	"github.com/VincentSh1/RouteForge/internal/persistence"
	"github.com/VincentSh1/RouteForge/internal/provider"
)

type capturingHistoryRecorder struct {
	mu      sync.Mutex
	records []persistence.RequestRecord
	result  persistence.SubmitResult
}

func (r *capturingHistoryRecorder) Enabled() bool { return true }

func (r *capturingHistoryRecorder) Submit(record persistence.RequestRecord) persistence.SubmitResult {
	r.mu.Lock()
	r.records = append(r.records, record.Clone())
	r.mu.Unlock()
	if r.result != 0 {
		return r.result
	}
	return persistence.SubmitQueued
}

func (r *capturingHistoryRecorder) record(t *testing.T) persistence.RequestRecord {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.records) != 1 {
		t.Fatalf("history records = %d, want 1", len(r.records))
	}
	return r.records[0].Clone()
}

func attachHistory(service *Service, recorder *capturingHistoryRecorder) {
	service.SetPersistence(recorder, func() (string, error) { return "rfreq_deterministic", nil })
}

func TestPersistenceRecordsSuccessfulSynchronousRequest(t *testing.T) {
	upstream := &usageCompletionProvider{name: "first", usage: openai.NewUsage(3, 4, 7)}
	service := New(upstream, testResolver())
	inputRate, outputRate := uint64(1_000_000), uint64(2_000_000)
	service.SetPricing(accounting.PriceBook{
		{Provider: "first", Model: "first-model"}: {InputMicroUSDPerMillion: &inputRate, OutputMicroUSDPerMillion: &outputRate},
	})
	recorder := &capturingHistoryRecorder{}
	attachHistory(service, recorder)

	if _, err := service.Complete(context.Background(), validRequest()); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	record := recorder.record(t)
	if record.RequestID != "rfreq_deterministic" || record.Outcome != "success" || record.Streaming ||
		record.AttemptCount != 1 || record.FallbackCount != 0 || value(record.InitialProvider) != "first" || value(record.FinalProvider) != "first" {
		t.Fatalf("request record = %+v", record)
	}
	attempt := record.Attempts[0]
	if attempt.Provider != "first" || attempt.ResolvedProviderModel != "first-model" || attempt.Fallback ||
		attempt.Outcome != "success" || value(attempt.InputTokens) != 3 || value(attempt.OutputTokens) != 4 ||
		value(attempt.TotalTokens) != 7 || attempt.EstimatedCostMicroUSD == nil {
		t.Fatalf("attempt record = %+v", attempt)
	}
}

func TestPersistenceRecordsFallbackAttemptsIndependently(t *testing.T) {
	first := &usageCompletionProvider{
		name: "first", usage: openai.NewUsage(5, 1, 6),
		err: provider.NewError(provider.ErrorTimeout, "first", errors.New("private failure")),
	}
	second := &usageCompletionProvider{name: "second", usage: openai.NewUsage(5, 3, 8)}
	service := NewAuto(testResolver(), first, second)
	recorder := &capturingHistoryRecorder{}
	attachHistory(service, recorder)

	if _, err := service.Complete(context.Background(), validRequest()); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	record := recorder.record(t)
	if record.AttemptCount != 2 || record.FallbackCount != 1 || value(record.InitialProvider) != "first" || value(record.FinalProvider) != "second" {
		t.Fatalf("request record = %+v", record)
	}
	if record.Attempts[0].Outcome != "timeout" || record.Attempts[0].Fallback ||
		record.Attempts[1].Outcome != "success" || !record.Attempts[1].Fallback ||
		value(record.Attempts[0].TotalTokens) != 6 || value(record.Attempts[1].TotalTokens) != 8 {
		t.Fatalf("attempts = %+v", record.Attempts)
	}
}

func TestPersistenceStreamingTTFCAndLaterFailure(t *testing.T) {
	clock := newManualClock()
	upstream := &timedStreamingProvider{name: "first", clock: clock, steps: []streamTelemetryStep{
		{advance: time.Millisecond, chunk: provider.StreamChunk{Usage: openai.NewUsage(4, 2, 6)}},
		{advance: 2 * time.Millisecond, chunk: provider.StreamChunk{Content: "partial"}},
		{advance: time.Millisecond, err: provider.NewError(provider.ErrorUnavailable, "first", errors.New("private failure"))},
	}}
	service := New(upstream, testResolver())
	service.now = clock.Now
	service.telemetry = newTelemetryTracker([]string{"first"}, 4, clock.Now)
	recorder := &capturingHistoryRecorder{}
	attachHistory(service, recorder)

	err := service.Stream(context.Background(), streamRequest(), func(provider.StreamChunk) error { return nil })
	if err == nil {
		t.Fatal("Stream() error = nil")
	}
	record := recorder.record(t)
	attempt := record.Attempts[0]
	if !record.Streaming || record.Outcome != "unavailable" || record.FinalProvider != nil ||
		attempt.Outcome != "unavailable" || attempt.TTFCUS == nil || *attempt.TTFCUS != 3000 ||
		value(attempt.TotalTokens) != 6 {
		t.Fatalf("request=%+v attempt=%+v", record, attempt)
	}
}

func TestPersistenceStreamingCancellationRetainsUsage(t *testing.T) {
	upstream := &streamingTestProvider{name: "first", chunks: []provider.StreamChunk{
		{Usage: openai.NewUsage(2, 1, 3)}, {Content: "content"},
	}}
	service := New(upstream, testResolver())
	recorder := &capturingHistoryRecorder{}
	attachHistory(service, recorder)

	err := service.Stream(context.Background(), streamRequest(), func(provider.StreamChunk) error { return context.Canceled })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Stream() error = %v", err)
	}
	record := recorder.record(t)
	if record.Outcome != "cancellation" || record.Attempts[0].Outcome != "cancellation" ||
		record.Attempts[0].TTFCUS == nil || value(record.Attempts[0].TotalTokens) != 3 {
		t.Fatalf("record = %+v", record)
	}
}

func TestPersistenceMissingUsageAndCostRemainUnavailable(t *testing.T) {
	service := New(&recordingProvider{name: "recording"}, testResolver())
	recorder := &capturingHistoryRecorder{}
	attachHistory(service, recorder)
	if _, err := service.Complete(context.Background(), validRequest()); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	attempt := recorder.record(t).Attempts[0]
	if attempt.InputTokens != nil || attempt.OutputTokens != nil || attempt.TotalTokens != nil || attempt.EstimatedCostMicroUSD != nil || attempt.TTFCUS != nil {
		t.Fatalf("unavailable values were fabricated: %+v", attempt)
	}
}

func TestPersistenceFailureCannotChangeInferenceOrHealth(t *testing.T) {
	upstream := &recordingProvider{name: "recording"}
	service := New(upstream, testResolver())
	recorder := &capturingHistoryRecorder{result: persistence.SubmitQueueFull}
	attachHistory(service, recorder)
	response, err := service.Complete(context.Background(), validRequest())
	if err != nil || response.ID != "recording" || !service.ProviderEligible("recording") {
		t.Fatalf("response=%+v error=%v eligible=%v", response, err, service.ProviderEligible("recording"))
	}
}

func TestPersistenceRecordsFailedRequestWithoutProviderAttempt(t *testing.T) {
	service := New(&recordingProvider{}, testResolver())
	recorder := &capturingHistoryRecorder{}
	attachHistory(service, recorder)
	_, err := service.Complete(context.Background(), openai.ChatCompletionRequest{})
	if !errors.Is(err, ErrModelRequired) {
		t.Fatalf("Complete() error = %v", err)
	}
	record := recorder.record(t)
	if record.Outcome != "invalid_request" || record.AttemptCount != 0 || record.InitialProvider != nil || record.FinalProvider != nil {
		t.Fatalf("record = %+v", record)
	}
}

func TestPersistenceIDGenerationFailureDoesNotChangeInference(t *testing.T) {
	service := New(&recordingProvider{}, testResolver())
	service.SetPersistence(&capturingHistoryRecorder{}, func() (string, error) { return "", io.ErrUnexpectedEOF })
	if _, err := service.Complete(context.Background(), validRequest()); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
}

func value[T any](pointer *T) T {
	if pointer == nil {
		var zero T
		return zero
	}
	return *pointer
}

func TestPersistenceSuccessfulStreamAndPrecommitFallback(t *testing.T) {
	first := &streamingTestProvider{name: "first", err: provider.NewError(provider.ErrorTimeout, "first", errors.New("private failure"))}
	second := &streamingTestProvider{name: "second", chunks: []provider.StreamChunk{
		{Content: "synthetic content"}, {Usage: openai.NewUsage(2, 3, 5)},
	}}
	service := NewAuto(testResolver(), first, second)
	recorder := &capturingHistoryRecorder{}
	attachHistory(service, recorder)
	request := streamRequest()
	if err := service.Stream(context.Background(), request, func(provider.StreamChunk) error { return nil }); err != nil {
		t.Fatal(err)
	}
	record := recorder.record(t)
	if record.Outcome != "success" || !record.Streaming || record.AttemptCount != 2 || record.FallbackCount != 1 ||
		value(record.FinalProvider) != "second" || record.Attempts[0].TTFCUS != nil || record.Attempts[1].TTFCUS == nil ||
		value(record.Attempts[1].TotalTokens) != 5 || record.Attempts[1].ResolvedProviderModel != "second-model" ||
		request.Model != streamRequest().Model {
		t.Fatalf("unexpected streaming history: %+v", record)
	}
}

func TestPersistenceDisabledDoesNotGenerateIDs(t *testing.T) {
	service := New(&recordingProvider{name: "recording"}, testResolver())
	service.SetPersistence(persistence.NoopRecorder{}, func() (string, error) {
		t.Fatal("disabled persistence generated an ID")
		return "", nil
	})
	if _, err := service.Complete(context.Background(), validRequest()); err != nil {
		t.Fatal(err)
	}
}

type failedHistoryStore struct{}

func TestPersistenceSkippedCircuitDoesNotCreateAttempt(t *testing.T) {
	first := &recordingProvider{name: "first"}
	second := &recordingProvider{name: "second"}
	service := NewAutoWithCircuitBreaker(testResolver(), CircuitConfig{FailureThreshold: 1, OpenDuration: time.Minute}, first, second)
	trial, _ := service.health.begin("first")
	trial.failure()
	recorder := &capturingHistoryRecorder{}
	attachHistory(service, recorder)
	if _, err := service.Complete(context.Background(), validRequest()); err != nil {
		t.Fatal(err)
	}
	record := recorder.record(t)
	if record.AttemptCount != 1 || record.FallbackCount != 0 || record.Attempts[0].Provider != "second" {
		t.Fatalf("skipped provider created an attempt: %+v", record)
	}
}

func (failedHistoryStore) Write(context.Context, persistence.RequestRecord) error {
	return errors.New("synthetic database failure")
}
func (failedHistoryStore) Close() {}

func TestPersistenceDatabaseWriteFailureDoesNotAffectInference(t *testing.T) {
	outcomes := make(chan persistence.WriteOutcome, 1)
	recorder := persistence.NewAsyncRecorder(failedHistoryStore{}, 1, func(outcome persistence.WriteOutcome) { outcomes <- outcome })
	service := New(&recordingProvider{name: "recording"}, testResolver())
	service.SetPersistence(recorder, func() (string, error) { return "rfreq_test", nil })
	if _, err := service.Complete(context.Background(), validRequest()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := recorder.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if <-outcomes != persistence.OutcomeWriteError || !service.ProviderEligible("recording") {
		t.Fatal("database failure affected provider eligibility or was not observed")
	}
}
