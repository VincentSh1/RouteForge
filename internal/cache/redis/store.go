package redis

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/VincentSh1/RouteForge/internal/cache"
	redisclient "github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"
)

type Store struct{ client *redisclient.Client }

var configureLogging sync.Once

type silentLogger struct{}

func (silentLogger) Printf(context.Context, string, ...interface{}) {}

// New validates configuration without requiring Redis availability at startup.
func New(rawURL string) (*Store, error) {
	if err := cache.ValidateURL(rawURL); err != nil {
		return nil, err
	}
	options, err := redisclient.ParseURL(rawURL)
	if err != nil {
		return nil, errors.New("invalid Redis configuration")
	}
	options.MaxRetries = -1
	options.DialerRetries = 1
	options.DialTimeout = cache.OperationTimeout
	options.ReadTimeout = cache.OperationTimeout
	options.WriteTimeout = cache.OperationTimeout
	options.PoolTimeout = cache.OperationTimeout
	options.ContextTimeoutEnabled = true
	options.PoolSize = 8
	options.MaxActiveConns = 8
	options.MinIdleConns = 0
	options.DisableIdentity = true
	options.Protocol = 2
	options.MaintNotificationsConfig = &maintnotifications.Config{Mode: maintnotifications.ModeDisabled}
	// go-redis exposes only a process-wide logger. Suppress its raw connection
	// diagnostics once at startup; bounded RouteForge metrics report failures.
	configureLogging.Do(func() { redisclient.SetLogger(silentLogger{}) })
	return &Store{client: redisclient.NewClient(options)}, nil
}

func (*Store) Enabled() bool  { return true }
func (s *Store) Close() error { return s.client.Close() }

func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, cache.OperationTimeout)
	defer cancel()
	// GETRANGE bounds the wire response even if an external writer inserted an
	// oversized value. One extra byte lets Decode detect and reject truncation.
	value, err := s.client.GetRange(ctx, key, 0, cache.MaxValueBytes).Bytes()
	if err != nil {
		return nil, errors.New("cache lookup failed")
	}
	return value, nil
}

func (s *Store) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if len(value) > cache.MaxValueBytes || ttl < time.Millisecond {
		return errors.New("invalid cache write")
	}
	ctx, cancel := context.WithTimeout(ctx, cache.OperationTimeout)
	defer cancel()
	if err := s.client.Set(ctx, key, value, ttl).Err(); err != nil {
		return errors.New("cache write failed")
	}
	return nil
}
