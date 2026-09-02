package benchmark

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/VincentSh1/RouteForge/internal/gateway"
)

const validFixture = `{
  "version": 1,
  "name": "custom",
  "mode": "non_streaming",
  "providers": [
    {"name": "first", "model": "first-model", "input_price_usd_per_million": "1.25", "output_price_usd_per_million": "2.5"},
    {"name": "second", "model": "second-model"}
  ],
  "inter_request_gap": "100ms",
  "circuit": {"failure_threshold": 2, "open_duration": "1s"},
  "routing": {"min_samples": 3, "sample_max_age": "30s", "exploration_interval": 2, "max_latency_over_fastest_percent": 20},
  "warmup": [],
  "requests": [
    {"id": "request-1", "providers": {
      "first": {"outcome": "success", "completion_latency": "220ms", "input_tokens": 10, "output_tokens": 2},
      "second": {"outcome": "timeout", "completion_latency": "2s"}
    }}
  ]
}`

func TestDecodeValidV1Fixture(t *testing.T) {
	scenario, err := DecodeScenario("fixtures/custom.json", strings.NewReader(validFixture))
	if err != nil {
		t.Fatalf("DecodeScenario() error = %v", err)
	}
	if scenario.Version != 1 || scenario.Name != "custom" || scenario.InterRequestGap != 100*time.Millisecond {
		t.Fatalf("scenario identity/config = %+v", scenario)
	}
	condition := scenario.Requests[0].Conditions["first"]
	if condition.CompletionLatency != 220*time.Millisecond || condition.Usage == nil || *condition.Usage.TotalTokens != 12 {
		t.Fatalf("condition = %+v", condition)
	}
	if scenario.Providers[0].Rates.InputMicroUSDPerMillion == nil || *scenario.Providers[0].Rates.InputMicroUSDPerMillion != 1_250_000 {
		t.Fatalf("pricing = %+v", scenario.Providers[0].Rates)
	}
}

func TestDecodeRejectsInvalidFixtures(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		want    string
	}{
		{name: "unsupported version", fixture: strings.Replace(validFixture, `"version": 1`, `"version": 2`, 1), want: "unsupported scenario version 2"},
		{name: "malformed JSON", fixture: `{`, want: "decode fixture"},
		{name: "unknown field", fixture: strings.Replace(validFixture, `"name": "custom"`, `"name": "custom", "typo": true`, 1), want: `unknown field "typo"`},
		{name: "invalid duration", fixture: strings.Replace(validFixture, `"220ms"`, `"soon"`, 1), want: "completion_latency"},
		{name: "negative duration", fixture: strings.Replace(validFixture, `"220ms"`, `"-1ms"`, 1), want: "completion_latency"},
		{name: "unknown outcome", fixture: strings.Replace(validFixture, `"outcome": "timeout"`, `"outcome": "mystery"`, 1), want: "unknown outcome"},
		{name: "negative tokens", fixture: strings.Replace(validFixture, `"input_tokens": 10`, `"input_tokens": -1`, 1), want: "input_tokens"},
		{name: "multiple objects", fixture: validFixture + `{}`, want: "one JSON object"},
		{name: "external URL field", fixture: strings.Replace(validFixture, `"outcome": "success"`, `"outcome": "success", "url": "https://example.invalid"`, 1), want: `unknown field "url"`},
		{name: "command field", fixture: strings.Replace(validFixture, `"outcome": "success"`, `"outcome": "success", "command": "echo unsafe"`, 1), want: `unknown field "command"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeScenario("fixtures/invalid.json", strings.NewReader(test.fixture))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("DecodeScenario() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestDecodeRejectsDuplicateAndIncompleteRequests(t *testing.T) {
	duplicate := strings.Replace(validFixture, `"warmup": []`, `"warmup": [{"id":"request-1","providers":{"first":{"outcome":"success","completion_latency":"1ms"},"second":{"outcome":"success","completion_latency":"1ms"}}}]`, 1)
	if _, err := DecodeScenario("fixtures/duplicate.json", strings.NewReader(duplicate)); err == nil || !strings.Contains(err.Error(), "request IDs must be unique") {
		t.Fatalf("duplicate error = %v", err)
	}

	missing := strings.Replace(validFixture, `,
      "second": {"outcome": "timeout", "completion_latency": "2s"}`, "", 1)
	if _, err := DecodeScenario("fixtures/missing.json", strings.NewReader(missing)); err == nil || !strings.Contains(err.Error(), `lacks provider "second"`) {
		t.Fatalf("missing provider error = %v", err)
	}

	extra := strings.Replace(validFixture,
		`"second": {"outcome": "timeout", "completion_latency": "2s"}`,
		`"second": {"outcome": "timeout", "completion_latency": "2s"}, "third": {"outcome": "success", "completion_latency": "1ms"}`,
		1,
	)
	if _, err := DecodeScenario("fixtures/extra.json", strings.NewReader(extra)); err == nil || !strings.Contains(err.Error(), `unknown provider "third"`) {
		t.Fatalf("unknown provider error = %v", err)
	}
}

func TestDecodeValidatesStreamingTiming(t *testing.T) {
	streaming := strings.NewReplacer(
		`"mode": "non_streaming"`, `"mode": "streaming"`,
		`"completion_latency": "220ms"`, `"ttfc": "220ms", "stream_duration": "200ms"`,
		`"outcome": "timeout", "completion_latency": "2s"`, `"outcome": "timeout", "stream_duration": "2s", "stream_failure_point": "before_commit"`,
	).Replace(validFixture)
	if _, err := DecodeScenario("fixtures/stream.json", strings.NewReader(streaming)); err == nil || !strings.Contains(err.Error(), "stream duration must not be less than TTFC") {
		t.Fatalf("stream timing error = %v", err)
	}

	contradictory := strings.Replace(streaming, `"ttfc": "220ms", "stream_duration": "200ms"`, `"ttfc": "100ms", "stream_duration": "200ms"`, 1)
	contradictory = strings.Replace(contradictory, `"stream_duration": "2s", "stream_failure_point": "before_commit"`, `"ttfc": "1ms", "stream_duration": "2s", "stream_failure_point": "before_commit"`, 1)
	if _, err := DecodeScenario("fixtures/stream.json", strings.NewReader(contradictory)); err == nil || !strings.Contains(err.Error(), "before_commit failure must not define ttfc") {
		t.Fatalf("pre-commit TTFC error = %v", err)
	}
}

func TestFixtureSizeIsBounded(t *testing.T) {
	oversized := strings.NewReader(strings.Repeat(" ", MaxFixtureSize+1))
	if _, err := DecodeScenario("fixtures/large.json", oversized); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized fixture error = %v", err)
	}
}

func TestAbsoluteFixturePathIsNotEchoedInErrors(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "missing.json")
	_, err := LoadScenarioFile(path)
	if err == nil {
		t.Fatal("LoadScenarioFile() error = nil")
	}
	if strings.Contains(err.Error(), directory) || !strings.Contains(err.Error(), "missing.json") {
		t.Fatalf("path error = %v", err)
	}
}

func TestMigratedFixturesPreservePhase5AResults(t *testing.T) {
	tests := []struct {
		name         string
		policy       string
		requests     uint64
		fallbacks    uint64
		circuitSkips uint64
		p50          int64
	}{
		{name: "stable", policy: gateway.RoutingPolicyDeterministic, requests: 12, p50: 300},
		{name: "degradation", policy: gateway.RoutingPolicyDeterministic, requests: 14, fallbacks: 3, circuitSkips: 4, p50: 180},
		{name: "rate_limit", policy: gateway.RoutingPolicyDeterministic, requests: 12, fallbacks: 2, circuitSkips: 2, p50: 210},
		{name: "streaming", policy: gateway.RoutingPolicyDeterministic, requests: 12, fallbacks: 1, p50: 100},
		{name: "cold_start", policy: gateway.RoutingPolicyLatency, requests: 18, p50: 190},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scenario, err := BuiltInScenario(test.name)
			if err != nil {
				t.Fatalf("BuiltInScenario() error = %v", err)
			}
			comparison, err := RunComparison(scenario, StateWarm, []string{test.policy})
			if err != nil {
				t.Fatalf("RunComparison() error = %v", err)
			}
			result := comparison.Results[0]
			p50 := result.P50LatencyMS
			if scenario.Mode == ModeStreaming {
				p50 = result.P50TTFCMS
			}
			if scenario.Version != 1 || comparison.ScenarioVersion != 1 || result.Requests != test.requests ||
				result.FallbackRequests != test.fallbacks || result.CircuitSkips != test.circuitSkips || value(p50) != test.p50 {
				t.Fatalf("fixture-backed result = %+v", result)
			}
		})
	}
}

func TestFixtureReplayJSONIsByteIdentical(t *testing.T) {
	scenario, err := BuiltInScenario("stable")
	if err != nil {
		t.Fatalf("BuiltInScenario() error = %v", err)
	}
	first, err := RunComparison(scenario, StateWarm, nil)
	if err != nil {
		t.Fatalf("RunComparison() error = %v", err)
	}
	secondScenario, err := BuiltInScenario("stable")
	if err != nil {
		t.Fatalf("second BuiltInScenario() error = %v", err)
	}
	second, err := RunComparison(secondScenario, StateWarm, nil)
	if err != nil {
		t.Fatalf("second RunComparison() error = %v", err)
	}
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	if string(firstJSON) != string(secondJSON) {
		t.Fatal("fixture replays produced different JSON")
	}
}
