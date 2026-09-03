package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/VincentSh1/RouteForge/internal/provider"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestTracingRecordsActualAttemptsAndFallback(t *testing.T) {
	first := &recordingProvider{name: "first", err: provider.NewError(provider.ErrorTimeout, "first", errors.New("private upstream detail"))}
	second := &recordingProvider{name: "second"}
	service := NewAuto(testResolver(), first, second)
	tracer, recorder := testTracer()
	service.SetTracer(tracer)

	ctx, requestSpan := tracer.Start(context.Background(), "routeforge.request")
	if _, err := service.Complete(ctx, validRequest()); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	requestSpan.End()

	attempts := spansNamed(recorder.Ended(), "routeforge.provider.attempt")
	if len(attempts) != 2 {
		t.Fatalf("attempt spans = %d, want 2", len(attempts))
	}
	assertSpanAttribute(t, attempts[0], "routeforge.provider.name", "first")
	assertSpanAttribute(t, attempts[0], "routeforge.provider.model", "first-model")
	assertSpanAttribute(t, attempts[0], "routeforge.provider.outcome", "timeout")
	assertSpanAttribute(t, attempts[0], "routeforge.provider.fallback", false)
	assertSpanAttribute(t, attempts[1], "routeforge.provider.name", "second")
	assertSpanAttribute(t, attempts[1], "routeforge.provider.model", "second-model")
	assertSpanAttribute(t, attempts[1], "routeforge.provider.outcome", "success")
	assertSpanAttribute(t, attempts[1], "routeforge.provider.fallback", true)
	for _, attempt := range attempts {
		if attempt.Parent().SpanID() != requestSpan.SpanContext().SpanID() {
			t.Fatal("provider attempt is not a request child")
		}
	}
	routing := spansNamed(recorder.Ended(), "routeforge.routing")
	if len(routing) != 1 {
		t.Fatalf("routing spans = %d", len(routing))
	}
	assertSpanAttribute(t, routing[0], "routeforge.routing.policy", RoutingPolicyDeterministic)
	assertSpanAttribute(t, routing[0], "routeforge.routing.candidates", int64(2))
	assertSpanAttribute(t, routing[0], "routeforge.routing.eligible_candidates", int64(2))
	assertSpanAttribute(t, routing[0], "routeforge.routing.initial_provider", "first")

	requests := spansNamed(recorder.Ended(), "routeforge.request")
	if len(requests) != 1 || countEvents(requests[0], "routeforge.fallback") != 1 {
		t.Fatalf("request fallback events = %d", countEvents(requests[0], "routeforge.fallback"))
	}
	if containsSpanText(recorder.Ended(), "private upstream detail") {
		t.Fatal("raw provider error was attached to a span")
	}
}

func TestTracingUsesExistingProviderOutcomeClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "timeout", err: provider.NewError(provider.ErrorTimeout, "recording", errors.New("detail")), want: "timeout"},
		{name: "unavailable", err: provider.NewError(provider.ErrorUnavailable, "recording", errors.New("detail")), want: "unavailable"},
		{name: "rate limit", err: provider.NewError(provider.ErrorRateLimited, "recording", errors.New("detail")), want: "rate_limited"},
		{name: "invalid request", err: provider.NewError(provider.ErrorInvalidRequest, "recording", errors.New("detail")), want: "invalid_request"},
		{name: "internal", err: provider.NewError(provider.ErrorInternal, "recording", errors.New("detail")), want: "internal"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := New(&recordingProvider{err: test.err}, testResolver())
			tracer, recorder := testTracer()
			service.SetTracer(tracer)
			_, _ = service.Complete(context.Background(), validRequest())
			attempts := spansNamed(recorder.Ended(), "routeforge.provider.attempt")
			if len(attempts) != 1 {
				t.Fatalf("attempt spans = %d", len(attempts))
			}
			assertSpanAttribute(t, attempts[0], "routeforge.provider.outcome", test.want)
		})
	}
}

func TestStreamingTracingRecordsFirstContentWithoutChunkSpans(t *testing.T) {
	first := &streamingTestProvider{name: "first", err: provider.NewError(provider.ErrorUnavailable, "first", errors.New("detail"))}
	second := &streamingTestProvider{name: "second", chunks: []provider.StreamChunk{
		{Role: "assistant"}, {Content: "private response"}, {Content: "more private response"}, {FinishReason: "stop"},
	}}
	service := NewAuto(testResolver(), first, second)
	tracer, recorder := testTracer()
	service.SetTracer(tracer)

	if err := service.Stream(context.Background(), streamRequest(), func(provider.StreamChunk) error { return nil }); err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	attempts := spansNamed(recorder.Ended(), "routeforge.provider.attempt")
	if len(attempts) != 2 || countEvents(attempts[0], "routeforge.first_content") != 0 || countEvents(attempts[1], "routeforge.first_content") != 1 {
		t.Fatalf("stream attempt spans/events = %d/%d", len(attempts), countEvents(attempts[1], "routeforge.first_content"))
	}
	if len(recorder.Ended()) != 3 {
		t.Fatalf("all spans = %d, want routing plus two attempts", len(recorder.Ended()))
	}
	if containsSpanText(recorder.Ended(), "private response") {
		t.Fatal("stream content was attached to a span")
	}
}

func TestStreamingTracingPostCommitFailureDoesNotFallback(t *testing.T) {
	first := &streamingTestProvider{
		name: "first", chunks: []provider.StreamChunk{{Content: "partial"}}, errAfter: 1,
		err: provider.NewError(provider.ErrorUnavailable, "first", errors.New("detail")),
	}
	second := &streamingTestProvider{name: "second", chunks: []provider.StreamChunk{{Content: "wrong"}}}
	service := NewAuto(testResolver(), first, second)
	tracer, recorder := testTracer()
	service.SetTracer(tracer)

	err := service.Stream(context.Background(), streamRequest(), func(provider.StreamChunk) error { return nil })
	if err == nil {
		t.Fatal("Stream() error = nil")
	}
	attempts := spansNamed(recorder.Ended(), "routeforge.provider.attempt")
	if len(attempts) != 1 || second.streamCalls != 0 {
		t.Fatalf("attempt spans=%d second calls=%d", len(attempts), second.streamCalls)
	}
	assertSpanAttribute(t, attempts[0], "routeforge.provider.outcome", "unavailable")
}

func TestTracingCancellationEndsAttemptWithoutErrorStatus(t *testing.T) {
	item := &streamingTestProvider{name: "recording", chunks: []provider.StreamChunk{{Content: "unused"}}}
	service := New(item, testResolver())
	tracer, recorder := testTracer()
	service.SetTracer(tracer)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = service.Stream(ctx, streamRequest(), func(provider.StreamChunk) error { return nil })

	attempts := spansNamed(recorder.Ended(), "routeforge.provider.attempt")
	if len(attempts) != 1 {
		t.Fatalf("attempt spans = %d", len(attempts))
	}
	assertSpanAttribute(t, attempts[0], "routeforge.provider.outcome", "cancellation")
	if attempts[0].Status().Code != 0 {
		t.Fatalf("cancellation status = %v, want unset", attempts[0].Status().Code)
	}
}

func TestCircuitSkippedProviderDoesNotCreateAttemptSpan(t *testing.T) {
	item := &recordingProvider{name: "recording", err: provider.NewError(provider.ErrorUnavailable, "recording", errors.New("detail"))}
	service := NewWithCircuitBreaker(item, testResolver(), CircuitConfig{FailureThreshold: 1, OpenDuration: defaultCircuitOpenDuration})
	tracer, recorder := testTracer()
	service.SetTracer(tracer)
	_, _ = service.Complete(context.Background(), validRequest())
	recorder.Reset()
	ctx, requestSpan := tracer.Start(context.Background(), "routeforge.request")
	_, _ = service.Complete(ctx, validRequest())
	requestSpan.End()
	if attempts := spansNamed(recorder.Ended(), "routeforge.provider.attempt"); len(attempts) != 0 {
		t.Fatalf("attempt spans = %d, want 0", len(attempts))
	}
	requests := spansNamed(recorder.Ended(), "routeforge.request")
	if len(requests) != 1 || countEvents(requests[0], "routeforge.provider.skipped") != 1 {
		t.Fatal("circuit admission denial was not visible on the request span")
	}
}

func TestHalfOpenAdmissionIsRecordedOnAttempt(t *testing.T) {
	item := &recordingProvider{name: "recording"}
	service := NewWithCircuitBreaker(item, testResolver(), CircuitConfig{FailureThreshold: 1, OpenDuration: defaultCircuitOpenDuration})
	health := service.health.providers[item.Name()]
	health.mu.Lock()
	health.state = circuitOpen
	health.openUntil = time.Now().Add(-time.Second)
	health.mu.Unlock()
	tracer, recorder := testTracer()
	service.SetTracer(tracer)
	if _, err := service.Complete(context.Background(), validRequest()); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	attempts := spansNamed(recorder.Ended(), "routeforge.provider.attempt")
	if len(attempts) != 1 {
		t.Fatalf("attempt spans = %d", len(attempts))
	}
	assertSpanAttribute(t, attempts[0], "routeforge.circuit.half_open_trial", true)
}

func TestTracingOmitsArbitraryProviderNativeModel(t *testing.T) {
	service := New(&recordingProvider{name: "recording"}, testResolver())
	tracer, recorder := testTracer()
	service.SetTracer(tracer)
	request := validRequest()
	request.Model = "client-controlled-model"
	if _, err := service.Complete(context.Background(), request); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	attempts := spansNamed(recorder.Ended(), "routeforge.provider.attempt")
	if len(attempts) != 1 {
		t.Fatalf("attempt spans = %d", len(attempts))
	}
	if spanHasAttribute(attempts[0], "routeforge.provider.model") {
		t.Fatal("client-controlled provider-native model was recorded")
	}
}

func testTracer() (trace.Tracer, *tracetest.SpanRecorder) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	return provider.Tracer("routeforge-test"), recorder
}

func spansNamed(spans []sdktrace.ReadOnlySpan, name string) []sdktrace.ReadOnlySpan {
	var matches []sdktrace.ReadOnlySpan
	for _, span := range spans {
		if span.Name() == name {
			matches = append(matches, span)
		}
	}
	return matches
}

func assertSpanAttribute(t *testing.T, span sdktrace.ReadOnlySpan, key string, want any) {
	t.Helper()
	for _, item := range span.Attributes() {
		if string(item.Key) == key {
			if got := item.Value.AsInterface(); got != want {
				t.Fatalf("attribute %s = %v, want %v", key, got, want)
			}
			return
		}
	}
	t.Fatalf("attribute %s is absent", key)
}

func countEvents(span sdktrace.ReadOnlySpan, name string) int {
	count := 0
	for _, event := range span.Events() {
		if event.Name == name {
			count++
		}
	}
	return count
}

func spanHasAttribute(span sdktrace.ReadOnlySpan, key string) bool {
	for _, item := range span.Attributes() {
		if string(item.Key) == key {
			return true
		}
	}
	return false
}

func containsSpanText(spans []sdktrace.ReadOnlySpan, text string) bool {
	for _, span := range spans {
		for _, item := range span.Attributes() {
			if item.Value.AsString() == text {
				return true
			}
		}
		for _, event := range span.Events() {
			if event.Name == text {
				return true
			}
			for _, item := range event.Attributes {
				if item.Value.AsString() == text {
					return true
				}
			}
		}
	}
	return false
}
