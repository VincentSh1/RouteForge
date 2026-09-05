package persistence

import (
	"context"
	"sync"
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

	mu        sync.Mutex
	accepting bool
	done      chan struct{}
	ctx       context.Context
	cancel    context.CancelFunc
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
		err := r.store.Write(ctx, record)
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
