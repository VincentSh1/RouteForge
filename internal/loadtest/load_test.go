package loadtest

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VincentSh1/RouteForge/internal/gateway"
	"github.com/VincentSh1/RouteForge/internal/httpapi"
	"github.com/VincentSh1/RouteForge/internal/observability"
	"github.com/VincentSh1/RouteForge/internal/provider/mock"
)

func fixture(t *testing.T, handler http.Handler) (Config, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	number, _ := strconv.Atoi(port)
	return Config{Port: number, Concurrency: 8, Requests: 32, Warmup: 4, MaxDuration: time.Second, RequestTimeout: time.Second, CacheMode: "disabled"}, server
}

func TestPercentiles(t *testing.T) {
	if Summarize(nil) != nil {
		t.Fatal("empty samples must be unavailable")
	}
	values := []time.Duration{5, 1, 4, 2, 3}
	before := append([]time.Duration(nil), values...)
	result := Summarize(values)
	if result.P50 != .000003 || result.P95 != .000005 || result.P99 != .000005 || !reflect.DeepEqual(before, values) {
		t.Fatal("incorrect nearest rank or mutation")
	}
	values = nil
	for i := 1; i <= 100; i++ {
		values = append(values, time.Duration(i)*time.Millisecond)
	}
	result = Summarize(values)
	if result.P50 != 50 || result.P95 != 95 || result.P99 != 99 {
		t.Fatal("incorrect tail percentiles")
	}
}

func TestRealMockHTTPAccountingAndWarmupExclusion(t *testing.T) {
	var calls atomic.Int64
	handler := httpapi.NewHandler(gateway.New(&mock.Provider{}, nil)).Routes()
	c, _ := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); handler.ServeHTTP(w, r) }))
	report, err := Run(context.Background(), c, 0)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 36 || report.Requests != 32 || report.Successes != 32 || report.Failures != 0 || report.ClientLatency.Samples != 32 || report.TTFC != nil || report.Warmup != 4 || report.RequestsPerSecond <= 0 {
		t.Fatal("incorrect measured accounting")
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, prohibited := range []string{"RouteForge load", "Hello from", "127.0.0.1", "mock-model", "password", "messages", "response_body"} {
		if strings.Contains(string(data), prohibited) {
			t.Fatal("report contains non-operational data")
		}
	}
	var decoded Report
	if json.Unmarshal(data, &decoded) != nil || decoded.Requests != 32 {
		t.Fatal("report cannot roundtrip")
	}
}

func TestFailuresAndWorkerBounds(t *testing.T) {
	var active, peak atomic.Int64
	c, _ := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		w.WriteHeader(503)
	}))
	client := Client(c.Concurrency)
	defer client.CloseIdleConnections()
	results, _ := phase(context.Background(), client, c, 31, 0)
	if len(results) != 31 || peak.Load() > int64(c.Concurrency) || active.Load() != 0 {
		t.Fatal("worker accounting not bounded")
	}
	for _, sample := range results {
		if sample.failure != "http_status" {
			t.Fatal("HTTP failure not recorded")
		}
	}
}

func TestStreamingFirstContentAndTerminalSemantics(t *testing.T) {
	for _, terminal := range []bool{true, false} {
		t.Run(strconv.FormatBool(terminal), func(t *testing.T) {
			c, _ := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, ": ping\n\ndata: {\"id\":\"chatcmpl-routeforge-mock\",\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n")
				http.NewResponseController(w).Flush()
				io.WriteString(w, "data: {\"id\":\"chatcmpl-routeforge-mock\",\"choices\":[{\"delta\":{\"content\":\"synthetic\"}}]}\n\n")
				if terminal {
					io.WriteString(w, "data: {\"id\":\"chatcmpl-routeforge-mock\",\"choices\":[{\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				}
			}))
			c.Streaming = true
			c.CacheMode = "bypass"
			client := Client(1)
			defer client.CloseIdleConnections()
			result := request(context.Background(), client, c, 0)
			if result.ttfc == nil || *result.ttfc > result.duration || (result.failure == "") != terminal {
				t.Fatal("TTFC or terminal semantics incorrect")
			}
		})
	}
}

func TestNoFirstContentIsUnavailable(t *testing.T) {
	c, _ := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, ": ping\n\ndata: {\"id\":\"chatcmpl-routeforge-mock\",\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	c.Streaming = true
	c.CacheMode = "bypass"
	client := Client(1)
	defer client.CloseIdleConnections()
	result := request(context.Background(), client, c, 0)
	if result.ttfc != nil || result.failure == "" {
		t.Fatal("fabricated content or successful stream")
	}
}

func TestMeasuredErrorsDoNotBecomeSuccesses(t *testing.T) {
	var calls atomic.Int64
	mockHandler := httpapi.NewHandler(gateway.New(&mock.Provider{}, nil)).Routes()
	c, _ := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 4 {
			mockHandler.ServeHTTP(w, r)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	report, err := Run(context.Background(), c, 0)
	if err != nil {
		t.Fatal(err)
	}
	if report.Successes != 0 || report.Failures != 32 || report.ErrorRate != 1 || report.SuccessRate != 0 || report.Errors["http_status"] != 32 || report.ClientLatency.Samples != 32 {
		t.Fatal("measured failures were hidden")
	}
}

func TestDeadlineStopsWorkers(t *testing.T) {
	c, _ := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	c.MaxDuration = 20 * time.Millisecond
	c.RequestTimeout = time.Second
	client := Client(c.Concurrency)
	defer client.CloseIdleConnections()
	results, _ := phase(context.Background(), client, c, 200, 0)
	if len(results) == 0 || len(results) > c.Concurrency {
		t.Fatal("work continued after deadline")
	}
	for _, result := range results {
		if result.failure != "timeout_or_cancellation" {
			t.Fatal("deadline not classified")
		}
	}
}

func TestBoundsAndRedirects(t *testing.T) {
	c, _ := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://example.invalid")
		w.WriteHeader(302)
	}))
	client := Client(1)
	defer client.CloseIdleConnections()
	if request(context.Background(), client, c, 0).failure != "http_status" {
		t.Fatal("redirect was followed")
	}
	for _, change := range []func(*Config){func(c *Config) { c.Concurrency = 65 }, func(c *Config) { c.Requests = 20001 }, func(c *Config) { c.MaxDuration = 6 * time.Minute }, func(c *Config) { c.Warmup = 0 }, func(c *Config) { c.CacheMode = "other" }} {
		invalid := c
		change(&invalid)
		if invalid.Validate() == nil {
			t.Fatal("invalid bounds accepted")
		}
	}
}

func TestObservedModesAndPersistenceDrops(t *testing.T) {
	c := Config{CacheMode: "warm_hit", Persistence: true}
	o, err := difference(counters{}, counters{Requests: 20, Hits: 20, Persisted: 15, Dropped: 5}, c, 20, true)
	if err != nil || !o.CacheModeVerified || o.Dropped != 5 || o.Persisted != 15 {
		t.Fatal("cache or persistence evidence hidden")
	}
	o, _ = difference(counters{}, counters{Requests: 20, Hits: 19, Misses: 1, Attempts: 1}, c, 20, true)
	if o.CacheModeVerified {
		t.Fatal("warm cache incorrectly verified")
	}
	if _, err = difference(counters{Requests: 2}, counters{}, c, 0, true); err == nil {
		t.Fatal("counter reset accepted")
	}
}

func TestMetricsParsing(t *testing.T) {
	c, _ := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "# TYPE routeforge_requests_total counter\nrouteforge_requests_total{streaming=\"false\"} 3\n# TYPE routeforge_cache_lookups_total counter\nrouteforge_cache_lookups_total{result=\"hit\"} 2\n")
	}))
	client := Client(1)
	defer client.CloseIdleConnections()
	got, err := readCounters(context.Background(), client, c.Port)
	if err != nil || got.Requests != 3 || got.Hits != 2 || got.Misses != 0 {
		t.Fatal("metrics parsing failed")
	}
}

type testCache struct {
	mu      sync.Mutex
	entries map[string][]byte
}

func (s *testCache) Enabled() bool { return true }
func (s *testCache) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.entries[key]...), nil
}
func (s *testCache) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[key] = append([]byte(nil), value...)
	return nil
}

func TestCacheModesVerifiedThroughRealGatewayMetrics(t *testing.T) {
	for _, mode := range []string{"miss", "warm_hit", "bypass"} {
		t.Run(mode, func(t *testing.T) {
			setup, err := observability.New(context.Background(), observability.Config{MetricsEnabled: true})
			if err != nil {
				t.Fatal(err)
			}
			defer setup.Shutdown(context.Background())
			service := gateway.New(&mock.Provider{}, nil)
			service.SetMetrics(setup.Metrics())
			service.SetCache(&testCache{entries: map[string][]byte{}}, time.Minute)
			handler := httpapi.TraceRequests(httpapi.NewHandler(service).Routes(), setup.Tracer(), setup.Propagator(), "deterministic", setup.Metrics())
			c, _ := fixture(t, handler)
			metrics, _ := fixture(t, setup.MetricsHandler())
			c.CacheMode = mode
			c.Streaming = mode == "bypass"
			report, err := Run(context.Background(), c, metrics.Port)
			if err != nil {
				t.Fatal(err)
			}
			if report.Successes != c.Requests || report.Observations == nil || !report.Observations.CacheModeVerified || report.Observations.Requests != int64(c.Requests) {
				t.Fatal("measured phase did not exclude warm-up or verify cache behavior")
			}
			if mode == "bypass" && (report.TTFC == nil || report.TTFC.Samples != c.Requests || report.StreamDuration == nil) {
				t.Fatal("streaming report missing timings")
			}
		})
	}
}
