package redis

import (
	"context"
	"testing"
	"time"

	"github.com/VincentSh1/RouteForge/internal/cache"
)

func TestClientUsesBoundedOperationsWithoutStartupConnection(t *testing.T) {
	store, err := New("redis://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	options := store.client.Options()
	if options.MaxRetries != 0 || options.DialerRetries != 1 || options.MaxActiveConns != 8 || !options.ContextTimeoutEnabled || options.ReadTimeout != cache.OperationTimeout {
		t.Fatal("unbounded client options")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Get(ctx, "synthetic"); err == nil {
		t.Fatal("canceled lookup succeeded")
	}
	if err := store.Set(ctx, "synthetic", nil, time.Minute); err == nil {
		t.Fatal("canceled write succeeded")
	}
}
