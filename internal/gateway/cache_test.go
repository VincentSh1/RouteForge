package gateway

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VincentSh1/RouteForge/internal/cache"
	"github.com/VincentSh1/RouteForge/internal/openai"
	"github.com/VincentSh1/RouteForge/internal/provider"
	"github.com/VincentSh1/RouteForge/internal/provider/mock"
)

type fakeCompletionCache struct {
	mu             sync.Mutex
	values         map[string][]byte
	gets, sets     int
	getErr, setErr error
	ttl            time.Duration
	afterGet       func()
}

func (*fakeCompletionCache) Enabled() bool { return true }
func (c *fakeCompletionCache) Get(_ context.Context, key string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gets++
	if c.afterGet != nil {
		c.afterGet()
	}
	return append([]byte(nil), c.values[key]...), c.getErr
}
func (c *fakeCompletionCache) Set(_ context.Context, key string, data []byte, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sets++
	c.ttl = ttl
	if c.setErr != nil {
		return c.setErr
	}
	if c.values == nil {
		c.values = make(map[string][]byte)
	}
	c.values[key] = append([]byte(nil), data...)
	return nil
}

type cacheTestProvider struct {
	mock.Provider
	name  string
	calls atomic.Int64
}

func (p *cacheTestProvider) Name() string { return p.name }
func (p *cacheTestProvider) Complete(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	p.calls.Add(1)
	if ctx.Err() != nil {
		return openai.ChatCompletionResponse{}, ctx.Err()
	}
	return p.Provider.Complete(ctx, request)
}

func TestCacheMissWriteHitPreservesOperationalAccounting(t *testing.T) {
	upstream := &cacheTestProvider{name: "first"}
	service := New(upstream, testResolver())
	store := &fakeCompletionCache{}
	service.SetCache(store, 2*time.Minute)
	metrics, reader := gatewayTestMetrics(t)
	service.SetMetrics(metrics)
	recorder := &capturingHistoryRecorder{}
	attachHistory(service, recorder)
	first, err := service.Complete(context.Background(), validRequest())
	if err != nil {
		t.Fatal(err)
	}
	telemetry, _ := service.TelemetrySnapshot("first")
	health, _ := service.health.snapshot("first")
	accounting := service.AccountingSnapshot()
	second, err := service.Complete(context.Background(), validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || upstream.calls.Load() != 1 || store.gets != 2 || store.sets != 1 || store.ttl != 2*time.Minute {
		t.Fatal("cache did not reuse the normalized response")
	}
	afterTelemetry, _ := service.TelemetrySnapshot("first")
	afterHealth, _ := service.health.snapshot("first")
	if !reflect.DeepEqual(telemetry, afterTelemetry) || !reflect.DeepEqual(health, afterHealth) || !reflect.DeepEqual(accounting, service.AccountingSnapshot()) {
		t.Fatal("hit changed provider observations")
	}
	if len(recorder.records) != 2 || recorder.records[0].CacheHit || !recorder.records[1].CacheHit || recorder.records[1].AttemptCount != 0 || len(recorder.records[1].Attempts) != 0 || value(recorder.records[1].InitialProvider) != "first" || value(recorder.records[1].FinalProvider) != "first" {
		t.Fatal("cache history contains fake attempt or wrong provider")
	}
	collected := collectGatewayMetrics(t, reader)
	assertGatewayIntSum(t, collected, "routeforge_cache_lookups", 2)
	assertGatewayIntSum(t, collected, "routeforge_cache_writes", 1)
	assertGatewayIntSum(t, collected, "routeforge_provider_attempts", 1)
	assertGatewayIntSum(t, collected, "routeforge_routing_selections", 2)
	assertGatewayIntSum(t, collected, "routeforge_tokens", 7)
	assertGatewayAttribute(t, collected["routeforge_cache_lookups"].Data, "result", "hit")
}

func TestCacheErrorsFailOpenAndUnsuccessfulResponsesAreNotWritten(t *testing.T) {
	for _, test := range []struct {
		name                         string
		getErr, setErr, providerErr  error
		malformed, oversized, cancel bool
		wantErr                      bool
		wantSets                     int
	}{
		{name: "get error", getErr: errors.New("private error"), wantSets: 1},
		{name: "set error", setErr: errors.New("private error"), wantSets: 1},
		{name: "malformed", malformed: true, wantSets: 1},
		{name: "provider failure", providerErr: provider.NewError(provider.ErrorTimeout, "first", errors.New("private error")), wantErr: true},
		{name: "cancellation", cancel: true, wantErr: true},
		{name: "oversized", oversized: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := &cacheTestProvider{name: "first", Provider: mock.Provider{Err: test.providerErr}}
			if test.oversized {
				p.ResponseText = strings.Repeat("x", cache.MaxValueBytes+1)
			}
			s := New(p, testResolver())
			store := &fakeCompletionCache{getErr: test.getErr, setErr: test.setErr}
			s.SetCache(store, cache.DefaultTTL)
			if test.malformed {
				key, _ := cache.Key(validRequest(), "first", "first-model")
				store.values = map[string][]byte{key: []byte(`{"version":99}`)}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.cancel {
				cancel()
			}
			_, err := s.Complete(ctx, validRequest())
			if (err != nil) != test.wantErr || store.sets != test.wantSets {
				t.Fatalf("err=%v writes=%d", err, store.sets)
			}
			if !test.wantErr && !s.ProviderEligible("first") {
				t.Fatal("cache error affected health")
			}
		})
	}
}

func TestStreamingBypassesCacheCompletely(t *testing.T) {
	s := New(&cacheTestProvider{name: "first"}, testResolver())
	store := &fakeCompletionCache{}
	s.SetCache(store, cache.DefaultTTL)
	if err := s.Stream(context.Background(), streamRequest(), func(provider.StreamChunk) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if store.gets != 0 || store.sets != 0 {
		t.Fatal("stream touched cache")
	}
}

func TestCacheHitAfterFailedAttemptKeepsOnlyRealAttempt(t *testing.T) {
	first := &cacheTestProvider{name: "first", Provider: mock.Provider{Err: provider.NewError(provider.ErrorUnavailable, "first", errors.New("failure"))}}
	second := &cacheTestProvider{name: "second"}
	s := NewAuto(testResolver(), first, second)
	store := &fakeCompletionCache{}
	s.SetCache(store, cache.DefaultTTL)
	key, _ := cache.Key(validRequest(), "second", "second-model")
	response, _ := second.Provider.Complete(context.Background(), validRequest())
	data, _ := cache.Encode(response)
	_ = store.Set(context.Background(), key, data, time.Minute)
	recorder := &capturingHistoryRecorder{}
	attachHistory(s, recorder)
	if _, err := s.Complete(context.Background(), validRequest()); err != nil {
		t.Fatal(err)
	}
	record := recorder.record(t)
	if first.calls.Load() != 1 || second.calls.Load() != 0 || !record.CacheHit || record.AttemptCount != 1 || record.Attempts[0].Outcome != "unavailable" || value(record.InitialProvider) != "first" || value(record.FinalProvider) != "second" {
		t.Fatal("fallback cache hit fabricated provider history")
	}
}

func TestCacheHitDoesNotReserveHalfOpenTrial(t *testing.T) {
	clock := newManualClock()
	p := &cacheTestProvider{name: "first"}
	s := New(p, testResolver())
	store := &fakeCompletionCache{}
	s.SetCache(store, cache.DefaultTTL)
	if _, err := s.Complete(context.Background(), validRequest()); err != nil {
		t.Fatal(err)
	}
	s.health = newHealthTracker([]string{"first"}, CircuitConfig{FailureThreshold: 1, OpenDuration: time.Second}, clock.Now)
	trial, _ := s.health.begin("first")
	trial.failure()
	gets := store.gets
	if _, err := s.Complete(context.Background(), validRequest()); err == nil || store.gets != gets {
		t.Fatal("OPEN provider served cache")
	}
	clock.Advance(time.Second)
	before, _ := s.health.snapshot("first")
	if _, err := s.Complete(context.Background(), validRequest()); err != nil {
		t.Fatal(err)
	}
	after, _ := s.health.snapshot("first")
	if !reflect.DeepEqual(before, after) || p.calls.Load() != 1 {
		t.Fatal("hit consumed half-open probe")
	}
	probe, allowed := s.health.begin("first")
	if !allowed || !probe.halfOpen {
		t.Fatal("actual probe was not available")
	}
	probe.ignore()
}

func TestConcurrentCacheHitsAreRaceFree(t *testing.T) {
	p := &cacheTestProvider{name: "first"}
	s := New(p, testResolver())
	s.SetCache(&fakeCompletionCache{}, cache.DefaultTTL)
	if _, err := s.Complete(context.Background(), validRequest()); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for i := 0; i < 32; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if _, err := s.Complete(context.Background(), validRequest()); err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	if p.calls.Load() != 1 {
		t.Fatal("hits invoked provider")
	}
}

func TestCacheRechecksCircuitAfterLookup(t *testing.T) {
	p := &cacheTestProvider{name: "first"}
	s := NewWithCircuitBreaker(p, testResolver(), CircuitConfig{FailureThreshold: 1, OpenDuration: time.Minute})
	store := &fakeCompletionCache{}
	s.SetCache(store, cache.DefaultTTL)
	if _, err := s.Complete(context.Background(), validRequest()); err != nil {
		t.Fatal(err)
	}
	store.afterGet = func() { trial, _ := s.health.begin("first"); trial.failure() }
	if _, err := s.Complete(context.Background(), validRequest()); err == nil {
		t.Fatal("cache restored an ineligible provider")
	}
	if p.calls.Load() != 1 {
		t.Fatal("ineligible provider invoked")
	}
}

func TestCacheTraceContainsOnlyCategoricalCacheData(t *testing.T) {
	p := &cacheTestProvider{name: "first", Provider: mock.Provider{ResponseText: "private synthetic answer"}}
	s := New(p, testResolver())
	s.SetCache(&fakeCompletionCache{}, cache.DefaultTTL)
	tracer, recorder := testTracer()
	s.SetTracer(tracer)
	request := validRequest()
	request.Messages[0].Content = "private synthetic prompt"
	for i := 0; i < 2; i++ {
		ctx, span := tracer.Start(context.Background(), "routeforge.request")
		if _, err := s.Complete(ctx, request); err != nil {
			t.Fatal(err)
		}
		span.End()
	}
	if len(spansNamed(recorder.Ended(), "routeforge.provider.attempt")) != 1 {
		t.Fatal("hit created attempt span")
	}
	key, _ := cache.Key(request, "first", "first-model")
	for _, sensitive := range []string{key, "private synthetic prompt", "private synthetic answer", "redis://"} {
		if containsSpanText(recorder.Ended(), sensitive) {
			t.Fatal("sensitive cache data entered tracing")
		}
	}
	requests := spansNamed(recorder.Ended(), "routeforge.request")
	if len(requests) != 2 || countEvents(requests[1], "routeforge.cache") != 1 {
		t.Fatal("missing cache event")
	}
}
