package persistence

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeStore struct {
	mu      sync.Mutex
	records []RequestRecord
	err     error
	started chan struct{}
	release chan struct{}
	closed  chan struct{}
}

func (s *fakeStore) Write(ctx context.Context, record RequestRecord) error {
	if s.started != nil {
		select {
		case s.started <- struct{}{}:
		default:
		}
	}
	if s.release != nil {
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	s.records = append(s.records, record)
	s.mu.Unlock()
	return s.err
}

func (s *fakeStore) Close() {
	if s.closed != nil {
		close(s.closed)
	}
}

func (s *fakeStore) snapshot() []RequestRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]RequestRecord(nil), s.records...)
}

func TestAsyncRecorderWritesAndDrainsOnShutdown(t *testing.T) {
	store := &fakeStore{closed: make(chan struct{})}
	var mu sync.Mutex
	var outcomes []WriteOutcome
	recorder := NewAsyncRecorder(store, 4, func(outcome WriteOutcome) {
		mu.Lock()
		outcomes = append(outcomes, outcome)
		mu.Unlock()
	})
	for _, id := range []string{"rfreq_one", "rfreq_two"} {
		if result := recorder.Submit(RequestRecord{RequestID: id}); result != SubmitQueued {
			t.Fatalf("Submit() = %v", result)
		}
	}
	if err := recorder.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if len(store.snapshot()) != 2 {
		t.Fatalf("written records = %d", len(store.snapshot()))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(outcomes) != 2 || outcomes[0] != OutcomeWritten || outcomes[1] != OutcomeWritten {
		t.Fatalf("outcomes = %v", outcomes)
	}
	select {
	case <-store.closed:
	default:
		t.Fatal("store was not closed")
	}
}

func TestAsyncRecorderQueueIsBounded(t *testing.T) {
	store := &fakeStore{started: make(chan struct{}, 1), release: make(chan struct{})}
	var mu sync.Mutex
	var outcomes []WriteOutcome
	recorder := NewAsyncRecorder(store, 1, func(outcome WriteOutcome) {
		mu.Lock()
		outcomes = append(outcomes, outcome)
		mu.Unlock()
	})
	if recorder.Submit(RequestRecord{RequestID: "rfreq_active"}) != SubmitQueued {
		t.Fatal("first record was not queued")
	}
	<-store.started
	if recorder.Submit(RequestRecord{RequestID: "rfreq_buffered"}) != SubmitQueued {
		t.Fatal("buffered record was not queued")
	}
	if recorder.Submit(RequestRecord{RequestID: "rfreq_dropped"}) != SubmitQueueFull {
		t.Fatal("full queue did not reject the record")
	}
	close(store.release)
	if err := recorder.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(outcomes) != 3 || outcomes[0] != OutcomeQueueFull {
		t.Fatalf("outcomes = %v", outcomes)
	}
}

func TestAsyncRecorderReportsWriteErrors(t *testing.T) {
	store := &fakeStore{err: errors.New("database unavailable")}
	result := make(chan WriteOutcome, 1)
	recorder := NewAsyncRecorder(store, 1, func(outcome WriteOutcome) { result <- outcome })
	recorder.Submit(RequestRecord{RequestID: "rfreq_failed"})
	if err := recorder.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if outcome := <-result; outcome != OutcomeWriteError {
		t.Fatalf("outcome = %q", outcome)
	}
}

func TestAsyncRecorderShutdownTimeoutAndConcurrentSubmit(t *testing.T) {
	store := &fakeStore{started: make(chan struct{}, 1), release: make(chan struct{})}
	recorder := NewAsyncRecorder(store, 128, nil)
	recorder.Submit(RequestRecord{RequestID: "rfreq_blocked"})
	<-store.started

	var wait sync.WaitGroup
	for i := 0; i < 64; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			recorder.Submit(RequestRecord{RequestID: "rfreq_concurrent"})
		}()
	}
	wait.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := recorder.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if result := recorder.Submit(RequestRecord{}); result != SubmitClosed {
		t.Fatalf("Submit() after shutdown = %v", result)
	}
	finishCtx, finishCancel := context.WithTimeout(context.Background(), time.Second)
	defer finishCancel()
	if err := recorder.Shutdown(finishCtx); err != nil {
		t.Fatalf("second Shutdown() error = %v", err)
	}
}

func TestAsyncRecorderCopiesSubmittedRecords(t *testing.T) {
	store := &fakeStore{started: make(chan struct{}, 1), release: make(chan struct{})}
	recorder := NewAsyncRecorder(store, 1, nil)
	value := uint64(7)
	record := RequestRecord{RequestID: "rfreq_original", Attempts: []AttemptRecord{{InputTokens: &value}}}
	recorder.Submit(record)
	<-store.started
	record.RequestID = "changed"
	*record.Attempts[0].InputTokens = 99
	close(store.release)
	if err := recorder.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	written := store.snapshot()[0]
	if written.RequestID != "rfreq_original" || *written.Attempts[0].InputTokens != 7 {
		t.Fatalf("submitted record was mutated: %+v", written)
	}
}
