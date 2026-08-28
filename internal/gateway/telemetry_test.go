package gateway

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/VincentSh1/RouteForge/internal/provider"
)

func TestTelemetryInitialStateAndOutcomeAccounting(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tracker := newTelemetryTracker([]string{"provider"}, 4, func() time.Time { return now })
	snapshot, ok := tracker.snapshot("provider")
	if !ok || snapshot.Attempts != 0 || len(snapshot.NonStreamingLatencies) != 0 {
		t.Fatalf("initial snapshot = %+v, found = %v", snapshot, ok)
	}

	outcomes := []providerOutcome{
		outcomeSuccess,
		outcomeTimeout,
		outcomeUnavailable,
		outcomeRateLimited,
		outcomeInvalidRequest,
		outcomeInternal,
		outcomeOtherFailure,
		outcomeCanceled,
	}
	for _, outcome := range outcomes {
		attempt := tracker.begin("provider")
		now = now.Add(time.Millisecond)
		attempt.finishNonStreaming(outcome)
	}
	snapshot, _ = tracker.snapshot("provider")
	if snapshot.Attempts != 8 || snapshot.Successes != 1 || snapshot.Failures != 6 || snapshot.Cancellations != 1 {
		t.Fatalf("outcome counts = %+v", snapshot)
	}
	if snapshot.Timeouts != 1 || snapshot.UnavailableFailures != 1 || snapshot.RateLimitFailures != 1 ||
		snapshot.InvalidRequestFailures != 1 || snapshot.InternalFailures != 1 || snapshot.OtherFailures != 1 {
		t.Fatalf("classified counts = %+v", snapshot)
	}
	if snapshot.LastAttempt.IsZero() || snapshot.LastSuccess.IsZero() || snapshot.LastFailure.IsZero() {
		t.Fatalf("timestamps were not recorded: %+v", snapshot)
	}
	if len(snapshot.NonStreamingLatencySamples) == 0 || snapshot.NonStreamingLatencySamples[0].ObservedAt.IsZero() {
		t.Fatal("non-streaming sample timestamp was not recorded")
	}
}

func TestTelemetryProviderStatisticsAreIndependent(t *testing.T) {
	tracker := newTelemetryTracker([]string{"first", "second"}, 4, nil)
	tracker.begin("first").finishNonStreaming(outcomeTimeout)
	tracker.begin("second").finishNonStreaming(outcomeSuccess)
	first, _ := tracker.snapshot("first")
	second, _ := tracker.snapshot("second")
	if first.Attempts != 1 || first.Timeouts != 1 || first.Successes != 0 {
		t.Fatalf("first snapshot = %+v", first)
	}
	if second.Attempts != 1 || second.Successes != 1 || second.Failures != 0 {
		t.Fatalf("second snapshot = %+v", second)
	}
}

func TestTelemetryRollingSamplesAreBoundedAndCopied(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tracker := newTelemetryTracker([]string{"provider"}, 3, func() time.Time { return now })
	for _, duration := range []time.Duration{time.Millisecond, 2 * time.Millisecond, 3 * time.Millisecond, 4 * time.Millisecond} {
		attempt := tracker.begin("provider")
		now = now.Add(duration)
		attempt.finishNonStreaming(outcomeSuccess)
	}
	snapshot, _ := tracker.snapshot("provider")
	want := []time.Duration{2 * time.Millisecond, 3 * time.Millisecond, 4 * time.Millisecond}
	if !equalDurations(snapshot.NonStreamingLatencies, want) {
		t.Fatalf("latencies = %v, want %v", snapshot.NonStreamingLatencies, want)
	}
	snapshot.NonStreamingLatencies[0] = time.Hour
	snapshot.NonStreamingLatencySamples[0].Duration = time.Hour
	again, _ := tracker.snapshot("provider")
	if !equalDurations(again.NonStreamingLatencies, want) {
		t.Fatalf("snapshot mutated tracker state: %v", again.NonStreamingLatencies)
	}
	if again.NonStreamingLatencySamples[0].Duration != want[0] {
		t.Fatalf("timestamped snapshot mutated tracker state: %+v", again.NonStreamingLatencySamples)
	}
}

func TestProviderOutcomeClassification(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		err  error
		want providerOutcome
	}{
		{name: "success", ctx: context.Background(), want: outcomeSuccess},
		{name: "timeout", ctx: context.Background(), err: provider.NewError(provider.ErrorTimeout, "provider", errors.New("timeout")), want: outcomeTimeout},
		{name: "unavailable", ctx: context.Background(), err: provider.NewError(provider.ErrorUnavailable, "provider", errors.New("unavailable")), want: outcomeUnavailable},
		{name: "rate limited", ctx: context.Background(), err: provider.NewError(provider.ErrorRateLimited, "provider", errors.New("limited")), want: outcomeRateLimited},
		{name: "invalid", ctx: context.Background(), err: provider.NewError(provider.ErrorInvalidRequest, "provider", errors.New("invalid")), want: outcomeInvalidRequest},
		{name: "canceled", ctx: canceledContext(), err: provider.NewError(provider.ErrorTimeout, "provider", context.Canceled), want: outcomeCanceled},
		{name: "typed timeout with canceled cause", ctx: context.Background(), err: provider.NewError(provider.ErrorTimeout, "provider", context.Canceled), want: outcomeTimeout},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyProviderOutcome(test.ctx, test.err); got != test.want {
				t.Fatalf("outcome = %v, want %v", got, test.want)
			}
		})
	}
}

func TestTelemetryConcurrentRecordingAndSnapshots(t *testing.T) {
	tracker := newTelemetryTracker([]string{"provider"}, 8, nil)
	const workers = 20
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			tracker.begin("provider").finishNonStreaming(outcomeSuccess)
			_, _ = tracker.snapshot("provider")
		}()
	}
	wait.Wait()
	snapshot, _ := tracker.snapshot("provider")
	if snapshot.Attempts != workers || snapshot.Successes != workers || len(snapshot.NonStreamingLatencies) != 8 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func equalDurations(got, want []time.Duration) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
