package loadtest

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/common/expfmt"
)

type counters struct {
	Requests          int64
	Attempts          int64
	Hits              int64
	Misses            int64
	LookupErrors      int64
	Writes            int64
	WriteErrors       int64
	Persisted         int64
	PersistenceErrors int64
	Dropped           int64
	QueueDepth        *int64
	Submitted         *int64
	WriteCount        *int64
	WriteSeconds      *float64
}

type Observations struct {
	Requests           int64 `json:"server_requests"`
	Attempts           int64 `json:"provider_attempts"`
	Hits               int64 `json:"cache_hits"`
	Misses             int64 `json:"cache_misses"`
	LookupErrors       int64 `json:"cache_lookup_errors"`
	Writes             int64 `json:"cache_writes"`
	WriteErrors        int64 `json:"cache_write_errors"`
	Persisted          int64 `json:"persistence_written"`
	PersistenceErrors  int64 `json:"persistence_write_errors"`
	Dropped            int64 `json:"persistence_queue_full"`
	PersistenceSettled bool  `json:"persistence_settled"`
	CacheModeVerified  bool  `json:"cache_mode_verified"`
}

func readCounters(parent context.Context, client *http.Client, port int) (counters, error) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/metrics", nil)
	response, err := client.Do(req)
	if err != nil {
		return counters{}, errors.New("metrics unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return counters{}, errors.New("metrics unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil || len(data) > 4<<20 {
		return counters{}, errors.New("invalid metrics response")
	}
	parser := expfmt.TextParser{}
	families, err := parser.TextToMetricFamilies(strings.NewReader(string(data)))
	if err != nil {
		return counters{}, errors.New("invalid metrics response")
	}
	sum := func(name, label, value string) int64 {
		var total int64
		for _, metric := range families[name].GetMetric() {
			matches := label == ""
			for _, pair := range metric.GetLabel() {
				if pair.GetName() == label && pair.GetValue() == value {
					matches = true
				}
			}
			if matches {
				total += int64(metric.GetCounter().GetValue())
			}
		}
		return total
	}
	result := counters{Requests: sum("routeforge_requests_total", "", ""), Attempts: sum("routeforge_provider_attempts_total", "", ""),
		Hits: sum("routeforge_cache_lookups_total", "result", "hit"), Misses: sum("routeforge_cache_lookups_total", "result", "miss"), LookupErrors: sum("routeforge_cache_lookups_total", "result", "error"),
		Writes: sum("routeforge_cache_writes_total", "result", "success"), WriteErrors: sum("routeforge_cache_writes_total", "result", "error"),
		Persisted: sum("routeforge_persistence_records_total", "outcome", "written"), PersistenceErrors: sum("routeforge_persistence_records_total", "outcome", "write_error"), Dropped: sum("routeforge_persistence_records_total", "outcome", "queue_full")}
	for name, dest := range map[string]**int64{"routeforge_persistence_submitted_total": &result.Submitted, "routeforge_persistence_writes_total": &result.WriteCount} {
		if _, ok := families[name]; ok {
			value := sum(name, "", "")
			*dest = &value
		}
	}
	if family, ok := families["routeforge_persistence_queue_depth"]; ok && len(family.Metric) == 1 {
		value := int64(family.Metric[0].GetGauge().GetValue())
		result.QueueDepth = &value
	}
	if family, ok := families["routeforge_persistence_write_duration_seconds_total"]; ok && len(family.Metric) == 1 {
		value := family.Metric[0].GetCounter().GetValue()
		result.WriteSeconds = &value
	}
	return result, nil
}

// Samples are cumulative deltas since the end of warm-up. Queue depth is an
// instantaneous observation, not the maximum between scrapes. Missing new
// instruments remain null when measuring the pre-instrumentation binary.
type PersistenceSample struct {
	ElapsedSeconds float64  `json:"elapsed_seconds"`
	Requests       int64    `json:"server_requests"`
	Written        int64    `json:"written"`
	Dropped        int64    `json:"queue_full"`
	Errors         int64    `json:"write_errors"`
	QueueDepth     *int64   `json:"queue_depth"`
	Submitted      *int64   `json:"submitted"`
	WriteCount     *int64   `json:"write_count"`
	WriteSeconds   *float64 `json:"write_seconds"`
}

type samplingResult struct {
	samples []PersistenceSample
	err     error
}

func samplePersistence(ctx context.Context, port int, before counters, result chan<- samplingResult) {
	client := Client(1)
	defer client.CloseIdleConnections()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	start := time.Now()
	r := samplingResult{}
	defer func() { result <- r }()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			after, err := readCounters(ctx, client, port)
			if err != nil {
				if ctx.Err() == nil {
					r.err = err
				}
				return
			}
			s := PersistenceSample{ElapsedSeconds: time.Since(start).Seconds(), Requests: after.Requests - before.Requests, Written: after.Persisted - before.Persisted, Dropped: after.Dropped - before.Dropped, Errors: after.PersistenceErrors - before.PersistenceErrors, QueueDepth: after.QueueDepth}
			if before.Submitted != nil && after.Submitted != nil {
				value := *after.Submitted - *before.Submitted
				s.Submitted = &value
			}
			if before.WriteCount != nil && after.WriteCount != nil {
				value := *after.WriteCount - *before.WriteCount
				s.WriteCount = &value
			}
			if before.WriteSeconds != nil && after.WriteSeconds != nil {
				value := *after.WriteSeconds - *before.WriteSeconds
				s.WriteSeconds = &value
			}
			r.samples = append(r.samples, s)
		}
	}
}

func settledCounters(ctx context.Context, client *http.Client, port int, before counters, requests int, persistence bool) (counters, bool, error) {
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		after, err := readCounters(ctx, client, port)
		if err != nil {
			return counters{}, false, err
		}
		settled := after.Requests-before.Requests >= int64(requests) && (!persistence || after.Persisted+after.PersistenceErrors+after.Dropped-before.Persisted-before.PersistenceErrors-before.Dropped >= int64(requests))
		if settled {
			return after, true, nil
		}
		select {
		case <-ctx.Done():
			return counters{}, false, errors.New("metrics observation canceled")
		case <-deadline.C:
			return after, false, nil
		case <-ticker.C:
		}
	}
}

func difference(before, after counters, c Config, requests int, settled bool) (Observations, error) {
	o := Observations{Requests: after.Requests - before.Requests, Attempts: after.Attempts - before.Attempts, Hits: after.Hits - before.Hits, Misses: after.Misses - before.Misses, LookupErrors: after.LookupErrors - before.LookupErrors, Writes: after.Writes - before.Writes, WriteErrors: after.WriteErrors - before.WriteErrors, Persisted: after.Persisted - before.Persisted, PersistenceErrors: after.PersistenceErrors - before.PersistenceErrors, Dropped: after.Dropped - before.Dropped, PersistenceSettled: settled}
	for _, value := range []int64{o.Requests, o.Attempts, o.Hits, o.Misses, o.LookupErrors, o.Writes, o.WriteErrors, o.Persisted, o.PersistenceErrors, o.Dropped} {
		if value < 0 {
			return o, errors.New("server counters reset during measurement")
		}
	}
	if o.Requests == int64(requests) && o.LookupErrors == 0 && o.WriteErrors == 0 {
		switch c.CacheMode {
		case "warm_hit":
			o.CacheModeVerified = o.Hits == int64(requests) && o.Misses == 0 && o.Attempts == 0
		case "miss":
			o.CacheModeVerified = o.Misses == int64(requests) && o.Hits == 0 && o.Attempts == int64(requests) && o.Writes == int64(requests)
		case "disabled", "bypass":
			o.CacheModeVerified = o.Hits+o.Misses == 0 && o.Attempts == int64(requests)
		}
	}
	return o, nil
}
