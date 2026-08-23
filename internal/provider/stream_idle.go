package provider

import (
	"context"
	"io"
	"sync"
	"time"
)

type StreamIdleWatchdog struct {
	cancel   context.CancelFunc
	activity chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

func NewStreamIdleWatchdog(parent context.Context, timeout time.Duration) (context.Context, *StreamIdleWatchdog) {
	ctx, cancel := context.WithCancel(parent)
	watchdog := &StreamIdleWatchdog{
		cancel:   cancel,
		activity: make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
	go watchdog.run(ctx, timeout)
	return ctx, watchdog
}

func (w *StreamIdleWatchdog) run(ctx context.Context, timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		close(w.done)
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.activity:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(timeout)
		case <-timer.C:
			w.cancel()
			return
		}
	}
}

func (w *StreamIdleWatchdog) Activity() {
	select {
	case w.activity <- struct{}{}:
	default:
	}
}

func (w *StreamIdleWatchdog) Stop() {
	w.stopOnce.Do(w.cancel)
	<-w.done
}

func (w *StreamIdleWatchdog) Wrap(body io.ReadCloser) io.ReadCloser {
	return &activityReadCloser{ReadCloser: body, activity: w.Activity}
}

type activityReadCloser struct {
	io.ReadCloser
	activity func()
}

func (r *activityReadCloser) Read(data []byte) (int, error) {
	n, err := r.ReadCloser.Read(data)
	if n > 0 {
		r.activity()
	}
	return n, err
}
