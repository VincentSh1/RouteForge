package persistence

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

const (
	DefaultQueueCapacity = 256
	defaultWriteTimeout  = 5 * time.Second
)

type WriteOutcome string

const (
	OutcomeWritten    WriteOutcome = "written"
	OutcomeWriteError WriteOutcome = "write_error"
	OutcomeQueueFull  WriteOutcome = "queue_full"
)

type SubmitResult uint8

const (
	SubmitDisabled SubmitResult = iota
	SubmitQueued
	SubmitQueueFull
	SubmitClosed
)

type Recorder interface {
	Enabled() bool
	Submit(RequestRecord) SubmitResult
}

type NoopRecorder struct{}

func (NoopRecorder) Enabled() bool                     { return false }
func (NoopRecorder) Submit(RequestRecord) SubmitResult { return SubmitDisabled }

type Store interface {
	Write(context.Context, RequestRecord) error
	Close()
}

type OutcomeObserver func(WriteOutcome)

type AsyncRecorder struct {
	store    Store
	queue    chan RequestRecord
	observer OutcomeObserver

	mu         sync.Mutex
	accepting  bool
	done       chan struct{}
	ctx        context.Context
	cancel     context.CancelFunc
	submitted  atomic.Int64
	writeCount atomic.Int64
	writeNanos atomic.Int64
}

// Stats is an observational snapshot. Counters are individually atomic; a
// concurrent completion can occur between reads. QueueDepth excludes the write
// in flight. No record metadata or database errors are retained here.
type Stats struct {
	Submitted     int64
	QueueDepth    int64
	WriteCount    int64
	WriteDuration time.Duration
}

func (r *AsyncRecorder) Stats() Stats {
	return Stats{Submitted: r.submitted.Load(), QueueDepth: int64(len(r.queue)), WriteCount: r.writeCount.Load(), WriteDuration: time.Duration(r.writeNanos.Load())}
}

func NewAsyncRecorder(store Store, capacity int, observer OutcomeObserver) *AsyncRecorder {
	if capacity <= 0 {
		capacity = DefaultQueueCapacity
	}
	ctx, cancel := context.WithCancel(context.Background())
	recorder := &AsyncRecorder{
		store: store, queue: make(chan RequestRecord, capacity), observer: observer,
		accepting: true, done: make(chan struct{}),
		ctx: ctx, cancel: cancel,
	}
	go recorder.run()
	return recorder
}

func (r *AsyncRecorder) Enabled() bool { return r != nil }

func (r *AsyncRecorder) Submit(record RequestRecord) SubmitResult {
	if r == nil {
		return SubmitDisabled
	}
	record = record.Clone()
	r.mu.Lock()
	if !r.accepting {
		r.mu.Unlock()
		return SubmitClosed
	}
	r.submitted.Add(1)
	select {
	case r.queue <- record:
		r.mu.Unlock()
		return SubmitQueued
	default:
		r.mu.Unlock()
		r.observe(OutcomeQueueFull)
		return SubmitQueueFull
	}
}

func (r *AsyncRecorder) Shutdown(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.accepting {
		r.accepting = false
		close(r.queue)
	}
	r.mu.Unlock()

	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		r.cancel()
		return ctx.Err()
	}
}

func (r *AsyncRecorder) run() {
	defer close(r.done)
	defer r.store.Close()
	defer r.cancel()
	for record := range r.queue {
		if r.ctx.Err() != nil {
			r.observe(OutcomeWriteError)
			continue
		}
		ctx, cancel := context.WithTimeout(r.ctx, defaultWriteTimeout)
		started := time.Now()
		err := r.store.Write(ctx, record)
		r.writeNanos.Add(int64(time.Since(started)))
		r.writeCount.Add(1)
		cancel()
		if err != nil {
			r.observe(OutcomeWriteError)
			continue
		}
		r.observe(OutcomeWritten)
	}
}

func (r *AsyncRecorder) observe(outcome WriteOutcome) {
	if r.observer != nil {
		r.observer(outcome)
	}
}
