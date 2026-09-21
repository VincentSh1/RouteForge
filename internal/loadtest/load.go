// Package loadtest measures real, local HTTP requests. It is independent of the
// virtual-clock routing benchmark and never initializes providers or storage.
package loadtest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/VincentSh1/RouteForge/internal/openai"
	"github.com/VincentSh1/RouteForge/internal/provider"
)

type Config struct {
	Port           int
	Concurrency    int
	Requests       int
	Warmup         int
	MaxDuration    time.Duration
	RequestTimeout time.Duration
	Streaming      bool
	CacheMode      string
	Persistence    bool
}

func (c Config) Validate() error {
	if c.Port < 1 || c.Port > 65535 || c.Concurrency < 1 || c.Concurrency > 64 || c.Requests < 1 || c.Requests > 20000 || c.Warmup < 1 || c.Warmup > 2000 || c.MaxDuration < time.Millisecond || c.MaxDuration > 5*time.Minute || c.RequestTimeout < time.Millisecond || c.RequestTimeout > 30*time.Second {
		return errors.New("invalid load bounds")
	}
	if c.Streaming && c.CacheMode != "bypass" || !c.Streaming && c.CacheMode != "miss" && c.CacheMode != "warm_hit" && c.CacheMode != "disabled" {
		return errors.New("invalid cache mode for request mode")
	}
	return nil
}

type Percentiles struct {
	Samples int     `json:"samples"`
	P50     float64 `json:"p50_ms"`
	P95     float64 `json:"p95_ms"`
	P99     float64 `json:"p99_ms"`
}

// Summarize uses nearest rank: sorted[ceil(p*n)-1]. No observations is null,
// not a fabricated zero. The input remains unchanged.
func Summarize(values []time.Duration) *Percentiles {
	if len(values) == 0 {
		return nil
	}
	ordered := append([]time.Duration(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	value := func(p float64) float64 {
		return float64(ordered[int(math.Ceil(p*float64(len(ordered))))-1]) / float64(time.Millisecond)
	}
	return &Percentiles{len(values), value(.50), value(.95), value(.99)}
}

type Report struct {
	ClientEnvironment     Environment    `json:"client_environment"`
	Version               int            `json:"version"`
	Scenario              string         `json:"scenario"`
	Concurrency           int            `json:"concurrency"`
	Requested             int            `json:"requested_requests"`
	Requests              int            `json:"requests"`
	NotStarted            int            `json:"not_started"`
	Warmup                int            `json:"warmup_requests_excluded"`
	Successes             int            `json:"successes"`
	Failures              int            `json:"failures"`
	Errors                map[string]int `json:"errors"`
	SuccessRate           float64        `json:"success_rate"`
	ErrorRate             float64        `json:"error_rate"`
	ElapsedSeconds        float64        `json:"elapsed_seconds"`
	RequestsPerSecond     float64        `json:"requests_per_second"`
	SuccessesPerSecond    float64        `json:"successes_per_second"`
	MaxDurationSeconds    float64        `json:"max_duration_seconds"`
	RequestTimeoutSeconds float64        `json:"request_timeout_seconds"`
	ClientLatency         *Percentiles   `json:"client_latency"`
	TTFC                  *Percentiles   `json:"streaming_ttfc"`
	StreamDuration        *Percentiles   `json:"streaming_duration"`
	CacheMode             string         `json:"cache_mode"`
	Persistence           bool           `json:"persistence_enabled"`
	Observations          *Observations  `json:"observations"`
}

type Environment struct {
	GoVersion    string `json:"go_version"`
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	LogicalCPUs  int    `json:"logical_cpus"`
}

type observation struct {
	duration time.Duration
	ttfc     *time.Duration
	failure  string
}

// Client deliberately ignores HTTP_PROXY and refuses redirects. Only a literal
// loopback target is constructed by Run; no URL or provider model is accepted.
func Client(concurrency int) *http.Client {
	return &http.Client{Transport: &http.Transport{MaxConnsPerHost: concurrency, MaxIdleConns: concurrency, MaxIdleConnsPerHost: concurrency, IdleConnTimeout: 30 * time.Second, MaxResponseHeaderBytes: 8192},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func Run(ctx context.Context, c Config, metricsPort int) (Report, error) {
	if err := c.Validate(); err != nil {
		return Report{}, err
	}
	if metricsPort < 0 || metricsPort > 65535 {
		return Report{}, errors.New("invalid metrics port")
	}
	client := Client(c.Concurrency)
	defer client.CloseIdleConnections()
	var baseline counters
	var err error
	if metricsPort != 0 {
		baseline, err = readCounters(ctx, client, metricsPort)
		if err != nil {
			return Report{}, err
		}
	}
	// Warm-hit requests all reuse one deterministic key; prime it serially to
	// avoid concurrent cold misses being mistaken for a warmed cache.
	warmCount := c.Warmup
	if c.CacheMode == "warm_hit" {
		if request(ctx, client, c, 0).failure != "" {
			return Report{}, errors.New("cache priming request failed")
		}
		warmCount++
	}
	warm, _ := phase(ctx, client, c, c.Warmup, 0)
	if len(warm) != c.Warmup {
		return Report{}, errors.New("warm-up did not complete")
	}
	for _, sample := range warm {
		if sample.failure != "" {
			return Report{}, errors.New("warm-up request failed")
		}
	}
	if metricsPort != 0 {
		var settled bool
		baseline, settled, err = settledCounters(ctx, client, metricsPort, baseline, warmCount, c.Persistence)
		if err != nil {
			return Report{}, err
		}
		if !settled {
			return Report{}, errors.New("warm-up counters did not settle")
		}
	}
	samples, elapsed := phase(ctx, client, c, c.Requests, 2001)
	r := Report{Version: 1, Scenario: "mock_sync", Concurrency: c.Concurrency, Requested: c.Requests, Requests: len(samples), NotStarted: c.Requests - len(samples), Warmup: warmCount, Errors: map[string]int{}, ElapsedSeconds: elapsed.Seconds(), CacheMode: c.CacheMode, Persistence: c.Persistence, MaxDurationSeconds: c.MaxDuration.Seconds(), RequestTimeoutSeconds: c.RequestTimeout.Seconds()}
	r.ClientEnvironment = Environment{runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU()}
	var latencies, ttfcs []time.Duration
	for _, sample := range samples {
		latencies = append(latencies, sample.duration)
		if sample.ttfc != nil {
			ttfcs = append(ttfcs, *sample.ttfc)
		}
		if sample.failure == "" {
			r.Successes++
		} else {
			r.Failures++
			r.Errors[sample.failure]++
		}
	}
	if r.Requests > 0 {
		r.SuccessRate = float64(r.Successes) / float64(r.Requests)
		r.ErrorRate = float64(r.Failures) / float64(r.Requests)
	}
	if elapsed > 0 {
		r.RequestsPerSecond = float64(r.Requests) / elapsed.Seconds()
		r.SuccessesPerSecond = float64(r.Successes) / elapsed.Seconds()
	}
	r.ClientLatency = Summarize(latencies)
	if c.Streaming {
		r.Scenario = "mock_stream"
		r.TTFC = Summarize(ttfcs)
		r.StreamDuration = Summarize(latencies)
	}
	if metricsPort != 0 {
		after, settled, err := settledCounters(ctx, client, metricsPort, baseline, r.Requests, c.Persistence)
		if err != nil {
			return Report{}, err
		}
		observed, err := difference(baseline, after, c, r.Requests, settled)
		if err != nil {
			return Report{}, err
		}
		r.Observations = &observed
	}
	return r, nil
}

// A closed-loop worker issues its next request only after the previous one
// terminates. This measures achieved throughput, not an open-loop arrival SLO.
func phase(parent context.Context, client *http.Client, c Config, count, offset int) ([]observation, time.Duration) {
	ctx, cancel := context.WithTimeout(parent, c.MaxDuration)
	defer cancel()
	var next atomic.Int64
	results := make(chan observation, c.Concurrency)
	var workers sync.WaitGroup
	start := time.Now()
	for range c.Concurrency {
		workers.Go(func() {
			for ctx.Err() == nil {
				index := int(next.Add(1)) - 1
				if index >= count {
					return
				}
				results <- request(ctx, client, c, offset+index)
			}
		})
	}
	go func() { workers.Wait(); close(results) }()
	samples := make([]observation, 0, count)
	for sample := range results {
		samples = append(samples, sample)
	}
	return samples, time.Since(start)
}

func request(parent context.Context, client *http.Client, c Config, index int) (result observation) {
	if c.CacheMode == "warm_hit" {
		index = 0
	}
	payload, _ := json.Marshal(openai.ChatCompletionRequest{Model: "mock-model", Stream: c.Streaming, Messages: []openai.Message{{Role: "user", Content: "RouteForge load " + strconv.Itoa(index)}}})
	ctx, cancel := context.WithTimeout(parent, c.RequestTimeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:"+strconv.Itoa(c.Port)+"/v1/chat/completions", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	defer func() {
		result.duration = time.Since(start)
		if ctx.Err() != nil {
			result.failure = "timeout_or_cancellation"
		}
	}()
	response, err := client.Do(req)
	if err != nil {
		result.failure = "transport"
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		result.failure = "http_status"
		return
	}
	body := io.LimitReader(response.Body, 1<<20)
	result.failure = "invalid_response"
	if !c.Streaming {
		var completion openai.ChatCompletionResponse
		decoder := json.NewDecoder(body)
		if decoder.Decode(&completion) == nil && decoder.Decode(new(any)) == io.EOF && completion.ID == "chatcmpl-routeforge-mock" && len(completion.Choices) > 0 && completion.Choices[0].FinishReason == "stop" {
			result.failure = ""
		}
		return
	}
	sse := provider.NewSSEReader(body)
	finished := false
	for {
		event, err := sse.Next()
		if err != nil {
			return
		}
		if len(event.Data) == 0 {
			continue
		}
		if string(event.Data) == "[DONE]" {
			if finished && result.ttfc != nil {
				result.failure = ""
			}
			return
		}
		var chunk openai.ChatCompletionChunk
		if json.Unmarshal(event.Data, &chunk) != nil || chunk.ID != "chatcmpl-routeforge-mock" {
			return
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" && result.ttfc == nil {
				duration := time.Since(start)
				result.ttfc = &duration
			}
			if choice.FinishReason != nil && *choice.FinishReason == "stop" {
				finished = true
			}
		}
	}
}
