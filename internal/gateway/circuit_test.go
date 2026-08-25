package gateway

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCircuitStateMachine(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tracker := newHealthTracker([]string{"provider"}, CircuitConfig{FailureThreshold: 2, OpenDuration: time.Minute}, func() time.Time { return now })

	assertCircuit(t, tracker, circuitClosed, 0)
	first, ok := tracker.begin("provider")
	if !ok {
		t.Fatal("closed circuit rejected an attempt")
	}
	first.failure()
	assertCircuit(t, tracker, circuitClosed, 1)
	snapshot, _ := tracker.snapshot("provider")
	if !snapshot.LastFailure.Equal(now) {
		t.Fatalf("last failure = %v, want %v", snapshot.LastFailure, now)
	}

	reset, _ := tracker.begin("provider")
	reset.success()
	assertCircuit(t, tracker, circuitClosed, 0)
	snapshot, _ = tracker.snapshot("provider")
	if !snapshot.LastSuccess.Equal(now) {
		t.Fatalf("last success = %v, want %v", snapshot.LastSuccess, now)
	}

	for range 2 {
		attempt, _ := tracker.begin("provider")
		attempt.failure()
	}
	assertCircuit(t, tracker, circuitOpen, 2)
	if _, ok := tracker.begin("provider"); ok {
		t.Fatal("open circuit admitted an attempt before cooldown")
	}

	now = now.Add(time.Minute)
	trial, ok := tracker.begin("provider")
	if !ok {
		t.Fatal("cooldown did not admit a half-open trial")
	}
	assertCircuitState(t, tracker, circuitHalfOpen)
	if _, ok := tracker.begin("provider"); ok {
		t.Fatal("half-open circuit admitted a second trial")
	}
	trial.failure()
	assertCircuitState(t, tracker, circuitOpen)

	now = now.Add(time.Minute)
	trial, _ = tracker.begin("provider")
	trial.success()
	assertCircuit(t, tracker, circuitClosed, 0)
}

func TestCircuitIgnoredHalfOpenTrialReleasesAdmission(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tracker := newHealthTracker([]string{"provider"}, CircuitConfig{FailureThreshold: 1, OpenDuration: time.Minute}, func() time.Time { return now })
	attempt, _ := tracker.begin("provider")
	attempt.failure()
	now = now.Add(time.Minute)
	trial, _ := tracker.begin("provider")
	trial.ignore()
	if _, ok := tracker.begin("provider"); !ok {
		t.Fatal("ignored half-open trial did not release admission")
	}
}

func TestCircuitAllowsOnlyOneConcurrentHalfOpenTrial(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tracker := newHealthTracker([]string{"provider"}, CircuitConfig{FailureThreshold: 1, OpenDuration: time.Minute}, func() time.Time { return now })
	attempt, _ := tracker.begin("provider")
	attempt.failure()
	now = now.Add(time.Minute)

	start := make(chan struct{})
	var admitted atomic.Int32
	var wait sync.WaitGroup
	for range 20 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			if _, ok := tracker.begin("provider"); ok {
				admitted.Add(1)
			}
		}()
	}
	close(start)
	wait.Wait()
	if got := admitted.Load(); got != 1 {
		t.Fatalf("admitted trials = %d, want 1", got)
	}
}

func assertCircuit(t *testing.T, tracker *healthTracker, state circuitState, failures int) {
	t.Helper()
	snapshot, ok := tracker.snapshot("provider")
	if !ok || snapshot.State != state || snapshot.ConsecutiveFailures != failures {
		t.Fatalf("snapshot = %+v, found = %v", snapshot, ok)
	}
}

func assertCircuitState(t *testing.T, tracker *healthTracker, state circuitState) {
	t.Helper()
	snapshot, _ := tracker.snapshot("provider")
	if snapshot.State != state {
		t.Fatalf("state = %q, want %q", snapshot.State, state)
	}
}
