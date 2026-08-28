package gateway

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VincentSh1/RouteForge/internal/model"
	"github.com/VincentSh1/RouteForge/internal/openai"
	"github.com/VincentSh1/RouteForge/internal/provider"
)

func TestProviderHealthFailureClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "timeout", err: provider.NewError(provider.ErrorTimeout, "provider", errors.New("timeout")), want: true},
		{name: "unavailable", err: provider.NewError(provider.ErrorUnavailable, "provider", errors.New("unavailable")), want: true},
		{name: "rate limited", err: provider.NewError(provider.ErrorRateLimited, "provider", errors.New("limited")), want: true},
		{name: "invalid request", err: provider.NewError(provider.ErrorInvalidRequest, "provider", errors.New("invalid"))},
		{name: "internal", err: provider.NewError(provider.ErrorInternal, "provider", errors.New("internal"))},
		{name: "model resolution", err: &model.ResolutionError{Kind: model.ErrorMissingMapping}},
		{name: "client cancellation", err: context.Canceled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := degradesProviderHealth(test.err); got != test.want {
				t.Fatalf("degradesProviderHealth() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestClientCancellationDoesNotDegradeProviderHealth(t *testing.T) {
	service := newExplicitHealthTestService(&recordingProvider{name: "first"}, nil)
	attempt, _ := service.health.begin("first")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	recordHealthOutcome(attempt, classifyProviderOutcome(ctx, provider.NewError(provider.ErrorTimeout, "first", context.Canceled)))
	assertProviderHealth(t, service, "first", circuitClosed, 0)
}

func TestModelResolutionFailureDoesNotDegradeProviderHealth(t *testing.T) {
	p := &recordingProvider{name: "first"}
	service := newExplicitHealthTestService(p, nil)
	req := validRequest()
	req.Model = "routeforge/unknown"
	_, _ = service.Complete(context.Background(), req)
	assertProviderHealth(t, service, "first", circuitClosed, 0)
	if p.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", p.calls)
	}
}

func TestAutoCircuitSkipsAndRecoversProvider(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	first := &recordingProvider{name: "first", err: unavailableError("first")}
	second := &recordingProvider{name: "second"}
	service := newAutoHealthTestService(&now, first, second)

	if _, err := service.Complete(context.Background(), validRequest()); err != nil {
		t.Fatalf("first Complete() error = %v", err)
	}
	assertProviderHealth(t, service, "first", circuitOpen, 1)
	first.err = nil
	if _, err := service.Complete(context.Background(), validRequest()); err != nil {
		t.Fatalf("second Complete() error = %v", err)
	}
	if first.calls != 1 || second.calls != 2 {
		t.Fatalf("calls before cooldown = %d,%d", first.calls, second.calls)
	}

	now = now.Add(time.Minute)
	response, err := service.Complete(context.Background(), validRequest())
	if err != nil || response.ID != "first" {
		t.Fatalf("half-open response = %+v, error = %v", response, err)
	}
	assertProviderHealth(t, service, "first", circuitClosed, 0)
	if first.calls != 2 || second.calls != 2 {
		t.Fatalf("calls after recovery = %d,%d", first.calls, second.calls)
	}
	if _, err := service.Complete(context.Background(), validRequest()); err != nil {
		t.Fatalf("post-recovery Complete() error = %v", err)
	}
	if first.calls != 3 || second.calls != 2 {
		t.Fatalf("normal ordering was not restored: %d,%d", first.calls, second.calls)
	}
}

func TestAutoFailedHalfOpenTrialReopensCircuit(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	first := &recordingProvider{name: "first", err: unavailableError("first")}
	second := &recordingProvider{name: "second"}
	service := newAutoHealthTestService(&now, first, second)
	_, _ = service.Complete(context.Background(), validRequest())
	now = now.Add(time.Minute)
	_, _ = service.Complete(context.Background(), validRequest())
	assertProviderHealth(t, service, "first", circuitOpen, 2)
	if first.calls != 2 || second.calls != 2 {
		t.Fatalf("calls = %d,%d", first.calls, second.calls)
	}
}

func TestAutoAllOpenReturnsUnavailableWithoutAttempts(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	first := &recordingProvider{name: "first", err: unavailableError("first")}
	second := &recordingProvider{name: "second", err: unavailableError("second")}
	service := newAutoHealthTestService(&now, first, second)
	_, _ = service.Complete(context.Background(), validRequest())

	_, err := service.Complete(context.Background(), validRequest())
	var providerErr *provider.Error
	if !errors.As(err, &providerErr) || providerErr.Kind != provider.ErrorUnavailable {
		t.Fatalf("Complete() error = %v", err)
	}
	if first.calls != 1 || second.calls != 1 {
		t.Fatalf("open providers were attempted: %d,%d", first.calls, second.calls)
	}
}

func TestExplicitCircuitFailsFastAndAllowsHalfOpenTrial(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p := &recordingProvider{name: "first", err: unavailableError("first")}
	service := newExplicitHealthTestService(p, &now)
	_, _ = service.Complete(context.Background(), validRequest())

	p.err = nil
	_, err := service.Complete(context.Background(), validRequest())
	if !errors.Is(err, ErrCircuitOpen) || p.calls != 1 {
		t.Fatalf("open Complete() error = %v, calls = %d", err, p.calls)
	}
	now = now.Add(time.Minute)
	if _, err := service.Complete(context.Background(), validRequest()); err != nil {
		t.Fatalf("half-open Complete() error = %v", err)
	}
	assertProviderHealth(t, service, "first", circuitClosed, 0)
}

func TestStreamingHealthOutcomesPreserveFallbackRules(t *testing.T) {
	t.Run("pre-commit failure falls back", func(t *testing.T) {
		now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		first := &streamingTestProvider{name: "first", err: unavailableError("first")}
		second := &streamingTestProvider{name: "second", chunks: []provider.StreamChunk{{Content: "fallback"}}}
		service := newAutoHealthTestService(&now, first, second)
		err := service.Stream(context.Background(), streamRequest(), func(provider.StreamChunk) error { return nil })
		if err != nil || first.streamCalls != 1 || second.streamCalls != 1 {
			t.Fatalf("Stream() error = %v, calls = %d,%d", err, first.streamCalls, second.streamCalls)
		}
		assertProviderHealth(t, service, "first", circuitOpen, 1)
		assertProviderHealth(t, service, "second", circuitClosed, 0)
	})

	t.Run("post-commit failure opens without fallback", func(t *testing.T) {
		now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		first := &streamingTestProvider{name: "first", chunks: []provider.StreamChunk{{Content: "partial"}}, errAfter: 1, err: unavailableError("first")}
		second := &streamingTestProvider{name: "second"}
		service := newAutoHealthTestService(&now, first, second)
		err := service.Stream(context.Background(), streamRequest(), func(provider.StreamChunk) error { return nil })
		if err == nil || second.streamCalls != 0 {
			t.Fatalf("Stream() error = %v, second calls = %d", err, second.streamCalls)
		}
		assertProviderHealth(t, service, "first", circuitOpen, 1)
	})

	t.Run("client cancellation is ignored", func(t *testing.T) {
		now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		first := &streamingTestProvider{name: "first", err: provider.NewError(provider.ErrorTimeout, "first", context.Canceled)}
		service := newAutoHealthTestService(&now, first)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_ = service.Stream(ctx, streamRequest(), func(provider.StreamChunk) error { return nil })
		assertProviderHealth(t, service, "first", circuitClosed, 0)
	})
}

func TestStreamingSkipsOpenProvider(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	first := &streamingTestProvider{name: "first"}
	second := &streamingTestProvider{name: "second", chunks: []provider.StreamChunk{{Content: "second"}}}
	service := newAutoHealthTestService(&now, first, second)
	attempt, _ := service.health.begin("first")
	attempt.failure()
	if err := service.Stream(context.Background(), streamRequest(), func(provider.StreamChunk) error { return nil }); err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	if first.streamCalls != 0 || second.streamCalls != 1 {
		t.Fatalf("stream calls = %d,%d", first.streamCalls, second.streamCalls)
	}
}

func TestSuccessfulHalfOpenStreamClosesOnlyAfterCompletion(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p := &blockingStreamingProvider{name: "first", emitted: make(chan struct{}), release: make(chan struct{})}
	service := newExplicitHealthTestService(p, &now)
	attempt, _ := service.health.begin("first")
	attempt.failure()
	now = now.Add(time.Minute)

	result := make(chan error, 1)
	go func() {
		result <- service.Stream(context.Background(), streamRequest(), func(provider.StreamChunk) error { return nil })
	}()
	<-p.emitted
	assertProviderHealth(t, service, "first", circuitHalfOpen, 1)
	close(p.release)
	if err := <-result; err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	assertProviderHealth(t, service, "first", circuitClosed, 0)
}

func TestConcurrentRequestsAdmitOneHalfOpenProbe(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p := &blockingCompletionProvider{name: "first", started: make(chan struct{}), release: make(chan struct{})}
	service := newExplicitHealthTestService(p, &now)
	attempt, _ := service.health.begin("first")
	attempt.failure()
	now = now.Add(time.Minute)

	const requests = 20
	start := make(chan struct{})
	results := make(chan error, requests)
	for range requests {
		go func() {
			<-start
			_, err := service.Complete(context.Background(), validRequest())
			results <- err
		}()
	}
	close(start)
	<-p.started
	for range requests - 1 {
		if err := <-results; !errors.Is(err, ErrCircuitOpen) {
			t.Fatalf("rejected request error = %v", err)
		}
	}
	if calls := p.calls.Load(); calls != 1 {
		t.Fatalf("provider calls = %d, want 1", calls)
	}
	close(p.release)
	if err := <-results; err != nil {
		t.Fatalf("half-open trial error = %v", err)
	}
}

type blockingCompletionProvider struct {
	name    string
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
	once    sync.Once
}

func (p *blockingCompletionProvider) Name() string { return p.name }

func (p *blockingCompletionProvider) Complete(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	p.calls.Add(1)
	p.once.Do(func() { close(p.started) })
	<-p.release
	return openai.ChatCompletionResponse{ID: p.name}, nil
}

type blockingStreamingProvider struct {
	name    string
	emitted chan struct{}
	release chan struct{}
}

func (p *blockingStreamingProvider) Name() string { return p.name }

func (p *blockingStreamingProvider) Complete(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{}, nil
}

func (p *blockingStreamingProvider) Stream(context.Context, openai.ChatCompletionRequest) (provider.Stream, error) {
	return &blockingStream{emitted: p.emitted, release: p.release}, nil
}

type blockingStream struct {
	emitted chan struct{}
	release chan struct{}
	index   int
}

func (s *blockingStream) Next() (provider.StreamChunk, error) {
	if s.index == 0 {
		s.index++
		close(s.emitted)
		return provider.StreamChunk{Content: "partial"}, nil
	}
	<-s.release
	return provider.StreamChunk{}, io.EOF
}

func (s *blockingStream) Close() error { return nil }

func newAutoHealthTestService(now *time.Time, providers ...provider.Provider) *Service {
	service := NewAutoWithCircuitBreaker(testResolver(), CircuitConfig{FailureThreshold: 1, OpenDuration: time.Minute}, providers...)
	service.health = trackerWithClock(providers, now)
	return service
}

func newExplicitHealthTestService(p provider.Provider, now *time.Time) *Service {
	service := NewWithCircuitBreaker(p, testResolver(), CircuitConfig{FailureThreshold: 1, OpenDuration: time.Minute})
	service.health = trackerWithClock([]provider.Provider{p}, now)
	return service
}

func trackerWithClock(providers []provider.Provider, now *time.Time) *healthTracker {
	names := make([]string, len(providers))
	for i, item := range providers {
		names[i] = item.Name()
	}
	clock := time.Now
	if now != nil {
		clock = func() time.Time { return *now }
	}
	return newHealthTracker(names, CircuitConfig{FailureThreshold: 1, OpenDuration: time.Minute}, clock)
}

func assertProviderHealth(t *testing.T, service *Service, name string, state circuitState, failures int) {
	t.Helper()
	snapshot, ok := service.health.snapshot(name)
	if !ok || snapshot.State != state || snapshot.ConsecutiveFailures != failures {
		t.Fatalf("health = %+v, found = %v", snapshot, ok)
	}
}

func unavailableError(name string) error {
	return provider.NewError(provider.ErrorUnavailable, name, errors.New("unavailable"))
}
