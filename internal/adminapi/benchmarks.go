package adminapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sync"

	"github.com/VincentSh1/RouteForge/internal/benchmark"
)

type benchmarkScenario struct {
	ID             string         `json:"id"`
	Version        int            `json:"version"`
	Mode           benchmark.Mode `json:"mode"`
	Requests       int            `json:"requests"`
	WarmupRequests int            `json:"warmup_requests"`
}

type benchmarkReport struct {
	once sync.Once
	data []byte
	err  error
}

// Only the bounded embedded catalog is executable. Each report is computed
// once per admin server; neither live services nor external stores are inputs.
func newBenchmarkHandler(load func(string) (benchmark.Scenario, error)) http.Handler {
	catalog := make([]benchmarkScenario, 0, len(benchmark.BuiltInScenarioNames))
	type entry struct {
		scenario benchmark.Scenario
		reports  map[benchmark.State]*benchmarkReport
	}
	entries := make(map[string]entry)
	var catalogError bool
	for _, name := range benchmark.BuiltInScenarioNames {
		scenario, err := load(name)
		if err != nil {
			catalogError = true
			break
		}
		catalog = append(catalog, benchmarkScenario{name, scenario.Version, scenario.Mode, len(scenario.Requests), len(scenario.Warmup)})
		entries[name] = entry{scenario, map[benchmark.State]*benchmarkReport{benchmark.StateWarm: {}, benchmark.StateCold: {}}}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			writeError(w, 405, "method_not_allowed", "method not allowed")
			return
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			writeError(w, 400, "invalid_query", "invalid benchmark query")
			return
		}
		state := benchmark.StateWarm
		if values, ok := query["state"]; ok {
			if len(values) != 1 || (values[0] != "warm" && values[0] != "cold") {
				writeError(w, 400, "invalid_query", "invalid benchmark query")
				return
			}
			state = benchmark.State(values[0])
		}
		for key := range query {
			if key != "state" {
				writeError(w, 400, "invalid_query", "invalid benchmark query")
				return
			}
		}
		name := r.PathValue("scenario")
		if name == "" && r.URL.RawQuery != "" {
			writeError(w, 400, "invalid_query", "invalid benchmark query")
			return
		}
		if catalogError {
			writeError(w, 503, "benchmark_unavailable", "benchmark unavailable")
			return
		}
		if name == "" {
			writeJSON(w, 200, struct {
				Scenarios []benchmarkScenario `json:"scenarios"`
			}{catalog})
			return
		}
		item, ok := entries[name]
		if !ok {
			writeError(w, 400, "invalid_scenario", "unknown built-in scenario")
			return
		}
		report := item.reports[state]
		report.once.Do(func() {
			result, err := benchmark.RunComparison(item.scenario, state, nil)
			if err != nil {
				report.err = err
				return
			}
			report.data, report.err = json.Marshal(result)
		})
		if report.err != nil {
			writeError(w, 503, "benchmark_unavailable", "benchmark unavailable")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(report.data)
	})
}
