package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VincentSh1/RouteForge/internal/benchmark"
	"github.com/VincentSh1/RouteForge/internal/gateway"
	"github.com/VincentSh1/RouteForge/internal/model"
	"github.com/VincentSh1/RouteForge/internal/openai"
	"github.com/VincentSh1/RouteForge/internal/provider/mock"
)

func TestBenchmarkCatalogAndComparisons(t *testing.T) {
	h := NewServer("", nil, nil).Handler
	list := get(h, "/admin/v1/benchmarks")
	var catalog struct {
		Scenarios []benchmarkScenario `json:"scenarios"`
	}
	if list.Code != 200 || json.Unmarshal(list.Body.Bytes(), &catalog) != nil || len(catalog.Scenarios) != len(benchmark.BuiltInScenarioNames) {
		t.Fatal("invalid catalog")
	}
	for _, entry := range catalog.Scenarios {
		for _, state := range []benchmark.State{benchmark.StateCold, benchmark.StateWarm} {
			path := "/admin/v1/benchmarks/" + entry.ID + "?state=" + string(state)
			first, second := get(h, path), get(h, path)
			scenario, _ := benchmark.BuiltInScenario(entry.ID)
			expected, err := benchmark.RunComparison(scenario, state, nil)
			data, _ := json.Marshal(expected)
			if err != nil || first.Code != 200 || !bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) || !bytes.Equal(first.Body.Bytes(), data) {
				t.Fatalf("comparison differs from harness: %s", entry.ID)
			}
			if first.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("missing cache control")
			}
		}
	}
	if !bytes.Equal(get(h, "/admin/v1/benchmarks/stable").Body.Bytes(), get(h, "/admin/v1/benchmarks/stable?state=warm").Body.Bytes()) {
		t.Fatal("wrong default state")
	}
}

func TestBenchmarkRejectsUnsafeInputs(t *testing.T) {
	h := NewServer("", nil, nil).Handler
	for _, path := range []string{"/admin/v1/benchmarks/unknown", "/admin/v1/benchmarks/stable?state=invalid", "/admin/v1/benchmarks/stable?state=warm&state=cold", "/admin/v1/benchmarks/stable?file=private.json", "/admin/v1/benchmarks/stable?url=https://example.invalid", "/admin/v1/benchmarks/stable?state=%zz", "/admin/v1/benchmarks?state=warm"} {
		w := get(h, path)
		if w.Code != 400 {
			t.Fatalf("invalid query accepted: %s", path)
		}
		if strings.Contains(w.Body.String(), "private.json") || strings.Contains(w.Body.String(), "example.invalid") {
			t.Fatal("input leaked")
		}
	}
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, "/admin/v1/benchmarks/stable", nil))
		if w.Code != 405 {
			t.Fatal("write allowed")
		}
	}
}

func TestBenchmarkSanitizesFailures(t *testing.T) {
	for _, loader := range []func(string) (benchmark.Scenario, error){
		func(string) (benchmark.Scenario, error) {
			return benchmark.Scenario{}, errors.New("private failure sentinel")
		},
		func(name string) (benchmark.Scenario, error) {
			s, _ := benchmark.BuiltInScenario(name)
			s.Version = 999
			return s, nil
		},
	} {
		h := newBenchmarkHandler(loader)
		r := httptest.NewRequest("GET", "/admin/v1/benchmarks/stable", nil)
		r.SetPathValue("scenario", "stable")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 503 || w.Body.String() != "{\"error\":{\"code\":\"benchmark_unavailable\",\"message\":\"benchmark unavailable\"}}\n" {
			t.Fatal("unsafe failure response")
		}
	}
}

func TestBenchmarkConcurrentRequestsDoNotTouchLiveGateway(t *testing.T) {
	live, err := gateway.NewAutoWithRouting(model.New(map[string]map[string]string{model.General: {"mock": "mock-model"}}), gateway.CircuitConfig{FailureThreshold: 3, OpenDuration: time.Minute}, gateway.RoutingConfig{Policy: "latency", MinSamples: 2, SampleMaxAge: time.Minute, ExplorationInterval: 3}, &mock.Provider{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = live.Complete(context.Background(), openai.ChatCompletionRequest{Model: model.General, Messages: []openai.Message{{Role: "user", Content: "synthetic test"}}})
	if err != nil {
		t.Fatal(err)
	}
	before := live.OperationsSnapshot(gateway.RoutingConfig{})
	telemetry, _ := live.TelemetrySnapshot("mock")
	accounting := live.AccountingSnapshot()
	h := NewServer("", nil, func() Overview { t.Error("benchmark accessed live snapshot"); return Overview{} }).Handler
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			state := "warm"
			if i%2 == 0 {
				state = "cold"
			}
			if get(h, "/admin/v1/benchmarks/stable?state="+state).Code != http.StatusOK {
				t.Error("benchmark failed")
			}
		}(i)
	}
	wg.Wait()
	after := live.OperationsSnapshot(gateway.RoutingConfig{})
	after.ObservedAt = before.ObservedAt
	afterTelemetry, _ := live.TelemetrySnapshot("mock")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(telemetry, afterTelemetry) || !reflect.DeepEqual(accounting, live.AccountingSnapshot()) {
		t.Fatal("benchmark mutated live gateway")
	}
}
