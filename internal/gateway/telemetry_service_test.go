package gateway

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/VincentSh1/RouteForge/internal/openai"
	"github.com/VincentSh1/RouteForge/internal/provider"
)

func TestNonStreamingTelemetryRecordsSuccessAndFailureLatency(t *testing.T) {
	clock := newManualClock()
	p := &timedCompletionProvider{name: "first", clock: clock, durations: []time.Duration{10 * time.Millisecond, 4 * time.Millisecond}}
	service := New(p, testResolver())
	service.telemetry = newTelemetryTracker([]string{"first"}, 4, clock.Now)

	if _, err := service.Complete(context.Background(), validRequest()); err != nil {
		t.Fatalf("successful Complete() error = %v", err)
	}
	p.err = provider.NewError(provider.ErrorUnavailable, "first", errors.New("unavailable"))
	_, _ = service.Complete(context.Background(), validRequest())

	snapshot, _ := service.TelemetrySnapshot("first")
	if snapshot.Attempts != 2 || snapshot.Successes != 1 || snapshot.Failures != 1 || snapshot.UnavailableFailures != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	want := []time.Duration{10 * time.Millisecond, 4 * time.Millisecond}
	if !equalDurations(snapshot.NonStreamingLatencies, want) {
		t.Fatalf("latencies = %v, want %v", snapshot.NonStreamingLatencies, want)
	}
}

func TestFallbackRecordsIndependentProviderTelemetry(t *testing.T) {
	clock := newManualClock()
	first := &timedCompletionProvider{
		name: "first", clock: clock, durations: []time.Duration{2 * time.Millisecond},
		err: provider.NewError(provider.ErrorTimeout, "first", errors.New("timeout")),
	}
	second := &timedCompletionProvider{name: "second", clock: clock, durations: []time.Duration{3 * time.Millisecond}}
	service := NewAuto(testResolver(), first, second)
	service.telemetry = newTelemetryTracker([]string{"first", "second"}, 4, clock.Now)

	if _, err := service.Complete(context.Background(), validRequest()); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	firstSnapshot, _ := service.TelemetrySnapshot("first")
	secondSnapshot, _ := service.TelemetrySnapshot("second")
	if firstSnapshot.Attempts != 1 || firstSnapshot.Timeouts != 1 || firstSnapshot.Successes != 0 {
		t.Fatalf("first snapshot = %+v", firstSnapshot)
	}
	if secondSnapshot.Attempts != 1 || secondSnapshot.Successes != 1 || secondSnapshot.Failures != 0 {
		t.Fatalf("second snapshot = %+v", secondSnapshot)
	}
}

func TestNonStreamingCancellationIsRecordedSeparately(t *testing.T) {
	clock := newManualClock()
	p := &timedCompletionProvider{
		name: "first", clock: clock, durations: []time.Duration{2 * time.Millisecond},
		err: provider.NewError(provider.ErrorTimeout, "first", context.Canceled),
	}
	service := New(p, testResolver())
	service.telemetry = newTelemetryTracker([]string{"first"}, 4, clock.Now)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = service.Complete(ctx, validRequest())
	snapshot, _ := service.TelemetrySnapshot("first")
	if snapshot.Attempts != 1 || snapshot.Cancellations != 1 || snapshot.Failures != 0 || snapshot.Timeouts != 0 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	assertProviderHealth(t, service, "first", circuitClosed, 0)
}

func TestCircuitSkippedProviderDoesNotRecordTelemetry(t *testing.T) {
	clock := newManualClock()
	first := &timedCompletionProvider{name: "first", clock: clock}
	second := &timedCompletionProvider{name: "second", clock: clock, durations: []time.Duration{time.Millisecond}}
	providers := []provider.Provider{first, second}
	service := NewAutoWithCircuitBreaker(testResolver(), CircuitConfig{FailureThreshold: 1, OpenDuration: time.Minute}, providers...)
	service.health = newHealthTracker([]string{"first", "second"}, CircuitConfig{FailureThreshold: 1, OpenDuration: time.Minute}, clock.Now)
	service.telemetry = newTelemetryTracker([]string{"first", "second"}, 4, clock.Now)
	attempt, _ := service.health.begin("first")
	attempt.failure()

	if _, err := service.Complete(context.Background(), validRequest()); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	firstSnapshot, _ := service.TelemetrySnapshot("first")
	secondSnapshot, _ := service.TelemetrySnapshot("second")
	if firstSnapshot.Attempts != 0 || first.calls != 0 {
		t.Fatalf("skipped provider telemetry = %+v, calls = %d", firstSnapshot, first.calls)
	}
	if secondSnapshot.Attempts != 1 || secondSnapshot.Successes != 1 {
		t.Fatalf("second snapshot = %+v", secondSnapshot)
	}
}

func TestSuccessfulStreamTelemetry(t *testing.T) {
	clock := newManualClock()
	p := &timedStreamingProvider{name: "first", clock: clock, steps: []streamTelemetryStep{
		{advance: 2 * time.Millisecond, chunk: provider.StreamChunk{Role: "assistant"}},
		{advance: 3 * time.Millisecond, chunk: provider.StreamChunk{Content: "hello"}},
		{advance: 4 * time.Millisecond, chunk: provider.StreamChunk{FinishReason: "stop"}},
		{advance: time.Millisecond, err: io.EOF},
	}}
	service := New(p, testResolver())
	service.telemetry = newTelemetryTracker([]string{"first"}, 4, clock.Now)

	err := service.Stream(context.Background(), streamRequest(), func(chunk provider.StreamChunk) error {
		snapshot, _ := service.TelemetrySnapshot("first")
		if chunk.Role != "" && len(snapshot.StreamingTimeToFirstContent) != 0 {
			t.Fatal("role-only chunk recorded time to first content")
		}
		if chunk.Content != "" && !equalDurations(snapshot.StreamingTimeToFirstContent, []time.Duration{5 * time.Millisecond}) {
			t.Fatalf("TTFC during stream = %v", snapshot.StreamingTimeToFirstContent)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	snapshot, _ := service.TelemetrySnapshot("first")
	if snapshot.Attempts != 1 || snapshot.Successes != 1 || snapshot.Failures != 0 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if !equalDurations(snapshot.StreamingTimeToFirstContent, []time.Duration{5 * time.Millisecond}) ||
		!equalDurations(snapshot.StreamingDurations, []time.Duration{10 * time.Millisecond}) {
		t.Fatalf("stream timings = TTFC %v, duration %v", snapshot.StreamingTimeToFirstContent, snapshot.StreamingDurations)
	}
}

func TestStreamFailureTelemetryBeforeAndAfterFirstContent(t *testing.T) {
	t.Run("before first content", func(t *testing.T) {
		clock := newManualClock()
		p := &timedStreamingProvider{name: "first", clock: clock, steps: []streamTelemetryStep{{
			advance: 7 * time.Millisecond,
			err:     provider.NewError(provider.ErrorUnavailable, "first", errors.New("unavailable")),
		}}}
		service := New(p, testResolver())
		service.telemetry = newTelemetryTracker([]string{"first"}, 4, clock.Now)
		_ = service.Stream(context.Background(), streamRequest(), func(provider.StreamChunk) error { return nil })
		snapshot, _ := service.TelemetrySnapshot("first")
		if snapshot.Attempts != 1 || snapshot.UnavailableFailures != 1 || len(snapshot.StreamingTimeToFirstContent) != 0 ||
			!equalDurations(snapshot.StreamingDurations, []time.Duration{7 * time.Millisecond}) {
			t.Fatalf("snapshot = %+v", snapshot)
		}
	})

	t.Run("after first content", func(t *testing.T) {
		clock := newManualClock()
		p := &timedStreamingProvider{name: "first", clock: clock, steps: []streamTelemetryStep{
			{advance: 3 * time.Millisecond, chunk: provider.StreamChunk{Content: "partial"}},
			{advance: 5 * time.Millisecond, err: provider.NewError(provider.ErrorTimeout, "first", errors.New("timeout"))},
		}}
		service := New(p, testResolver())
		service.telemetry = newTelemetryTracker([]string{"first"}, 4, clock.Now)
		_ = service.Stream(context.Background(), streamRequest(), func(provider.StreamChunk) error { return nil })
		snapshot, _ := service.TelemetrySnapshot("first")
		if snapshot.Attempts != 1 || snapshot.Timeouts != 1 || snapshot.Successes != 0 ||
			!equalDurations(snapshot.StreamingTimeToFirstContent, []time.Duration{3 * time.Millisecond}) ||
			!equalDurations(snapshot.StreamingDurations, []time.Duration{8 * time.Millisecond}) {
			t.Fatalf("snapshot = %+v", snapshot)
		}
	})
}

func TestStreamingCancellationIsRecordedSeparately(t *testing.T) {
	clock := newManualClock()
	p := &timedStreamingProvider{
		name: "first", clock: clock, startAdvance: 4 * time.Millisecond,
		streamErr: provider.NewError(provider.ErrorTimeout, "first", context.Canceled),
	}
	service := New(p, testResolver())
	service.telemetry = newTelemetryTracker([]string{"first"}, 4, clock.Now)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = service.Stream(ctx, streamRequest(), func(provider.StreamChunk) error { return nil })
	snapshot, _ := service.TelemetrySnapshot("first")
	if snapshot.Attempts != 1 || snapshot.Cancellations != 1 || snapshot.Failures != 0 || snapshot.Timeouts != 0 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	assertProviderHealth(t, service, "first", circuitClosed, 0)
}

func TestStreamingFallbackRecordsEachProvider(t *testing.T) {
	clock := newManualClock()
	first := &timedStreamingProvider{name: "first", clock: clock, steps: []streamTelemetryStep{{
		advance: 2 * time.Millisecond,
		err:     provider.NewError(provider.ErrorTimeout, "first", errors.New("timeout")),
	}}}
	second := &timedStreamingProvider{name: "second", clock: clock, steps: []streamTelemetryStep{
		{advance: 3 * time.Millisecond, chunk: provider.StreamChunk{Content: "fallback"}},
		{advance: time.Millisecond, err: io.EOF},
	}}
	service := NewAuto(testResolver(), first, second)
	service.telemetry = newTelemetryTracker([]string{"first", "second"}, 4, clock.Now)
	if err := service.Stream(context.Background(), streamRequest(), func(provider.StreamChunk) error { return nil }); err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	firstSnapshot, _ := service.TelemetrySnapshot("first")
	secondSnapshot, _ := service.TelemetrySnapshot("second")
	if firstSnapshot.Attempts != 1 || firstSnapshot.Timeouts != 1 || len(firstSnapshot.StreamingTimeToFirstContent) != 0 {
		t.Fatalf("first snapshot = %+v", firstSnapshot)
	}
	if secondSnapshot.Attempts != 1 || secondSnapshot.Successes != 1 ||
		!equalDurations(secondSnapshot.StreamingTimeToFirstContent, []time.Duration{3 * time.Millisecond}) {
		t.Fatalf("second snapshot = %+v", secondSnapshot)
	}
}

type manualClock struct {
	now time.Time
}

func newManualClock() *manualClock {
	return &manualClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *manualClock) Now() time.Time { return c.now }

func (c *manualClock) Advance(duration time.Duration) { c.now = c.now.Add(duration) }

type timedCompletionProvider struct {
	name      string
	clock     *manualClock
	durations []time.Duration
	err       error
	calls     int
}

func (p *timedCompletionProvider) Name() string { return p.name }

func (p *timedCompletionProvider) Complete(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if p.calls < len(p.durations) {
		p.clock.Advance(p.durations[p.calls])
	}
	p.calls++
	return openai.ChatCompletionResponse{ID: p.name}, p.err
}

type timedStreamingProvider struct {
	name         string
	clock        *manualClock
	startAdvance time.Duration
	streamErr    error
	steps        []streamTelemetryStep
	streamCalls  int
}

func (p *timedStreamingProvider) Name() string { return p.name }

func (p *timedStreamingProvider) Complete(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{}, nil
}

func (p *timedStreamingProvider) Stream(context.Context, openai.ChatCompletionRequest) (provider.Stream, error) {
	p.streamCalls++
	p.clock.Advance(p.startAdvance)
	if p.streamErr != nil {
		return nil, p.streamErr
	}
	return &timedStream{clock: p.clock, steps: p.steps}, nil
}

type streamTelemetryStep struct {
	advance time.Duration
	chunk   provider.StreamChunk
	err     error
}

type timedStream struct {
	clock *manualClock
	steps []streamTelemetryStep
	index int
}

func (s *timedStream) Next() (provider.StreamChunk, error) {
	step := s.steps[s.index]
	s.index++
	s.clock.Advance(step.advance)
	return step.chunk, step.err
}

func (s *timedStream) Close() error { return nil }
