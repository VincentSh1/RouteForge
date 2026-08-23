package provider

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestStreamIdleWatchdogTimesOutWithoutActivity(t *testing.T) {
	ctx, watchdog := NewStreamIdleWatchdog(context.Background(), 20*time.Millisecond)
	defer watchdog.Stop()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("watchdog did not time out")
	}
}

func TestStreamIdleWatchdogResetsOnReadActivity(t *testing.T) {
	ctx, watchdog := NewStreamIdleWatchdog(context.Background(), 100*time.Millisecond)
	body := watchdog.Wrap(io.NopCloser(strings.NewReader("abcd")))
	defer watchdog.Stop()

	for range 4 {
		time.Sleep(30 * time.Millisecond)
		if _, err := body.Read(make([]byte, 1)); err != nil {
			t.Fatalf("Read() error = %v", err)
		}
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("context ended despite activity: %v", err)
	}
}

func TestStreamIdleWatchdogPreservesParentCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	ctx, watchdog := NewStreamIdleWatchdog(parent, time.Second)
	cancel()
	defer watchdog.Stop()
	select {
	case <-ctx.Done():
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatalf("context error = %v", ctx.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("parent cancellation was not propagated")
	}
}
