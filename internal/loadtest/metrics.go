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
	return counters{Requests: sum("routeforge_requests_total", "", ""), Attempts: sum("routeforge_provider_attempts_total", "", ""),
		Hits: sum("routeforge_cache_lookups_total", "result", "hit"), Misses: sum("routeforge_cache_lookups_total", "result", "miss"), LookupErrors: sum("routeforge_cache_lookups_total", "result", "error"),
		Writes: sum("routeforge_cache_writes_total", "result", "success"), WriteErrors: sum("routeforge_cache_writes_total", "result", "error"),
		Persisted: sum("routeforge_persistence_records_total", "outcome", "written"), PersistenceErrors: sum("routeforge_persistence_records_total", "outcome", "write_error"), Dropped: sum("routeforge_persistence_records_total", "outcome", "queue_full")}, nil
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
