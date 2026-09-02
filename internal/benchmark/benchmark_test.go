package benchmark

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/VincentSh1/RouteForge/internal/gateway"
	"github.com/VincentSh1/RouteForge/internal/openai"
	"github.com/VincentSh1/RouteForge/internal/provider"
)

func TestBenchmarkIsReproducibleAndPolicyStateIsolated(t *testing.T) {
	scenario := coldStartScenario()
	first, err := RunComparison(scenario, StateCold, nil)
	if err != nil {
		t.Fatalf("RunComparison() error = %v", err)
	}
	second, err := RunComparison(scenario, StateCold, nil)
	if err != nil {
		t.Fatalf("RunComparison() second error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated results differ:\nfirst=%+v\nsecond=%+v", first, second)
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("json.Marshal(first) error = %v", err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("json.Marshal(second) error = %v", err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatal("repeated JSON reports differ")
	}

	latencyOnly, err := RunComparison(scenario, StateCold, []string{gateway.RoutingPolicyLatency})
	if err != nil {
		t.Fatalf("latency RunComparison() error = %v", err)
	}
	if !reflect.DeepEqual(first.Results[1], latencyOnly.Results[0]) {
		t.Fatal("telemetry, circuit, or exploration state leaked between policy runs")
	}
}

func TestEveryPolicyReceivesTheSameCounterfactualConditions(t *testing.T) {
	scenario := baseScenario("fairness", ModeNonStreaming)
	scenario.Warmup = nil
	scenario.Requests = []Request{
		{ID: "request-1", Conditions: map[string]ProviderCondition{
			"openai":    failedCompletion(100*time.Millisecond, provider.ErrorTimeout, 10, 2),
			"anthropic": completion(200*time.Millisecond, 11, 3),
		}},
		{ID: "request-2", Conditions: map[string]ProviderCondition{
			"openai":    completion(120*time.Millisecond, 12, 4),
			"anthropic": completion(180*time.Millisecond, 13, 5),
		}},
	}
	original := cloneRequests(scenario.Requests)

	for _, policy := range SupportedPolicies {
		run, err := runPolicy(scenario, StateCold, policy)
		if err != nil {
			t.Fatalf("runPolicy(%s) error = %v", policy, err)
		}
		for index, trace := range run.Trace {
			for _, attempt := range trace.Attempts {
				want := scenario.Requests[index].Conditions[attempt.Provider]
				if !reflect.DeepEqual(attempt.Condition, want) {
					t.Fatalf("policy %s request %s provider %s condition changed", policy, trace.ID, attempt.Provider)
				}
			}
		}
	}
	if !reflect.DeepEqual(scenario.Requests, original) {
		t.Fatal("benchmark execution mutated the shared scenario")
	}
}

func TestFallbackLatencyUsageAndCostIncludeEveryAttempt(t *testing.T) {
	scenario := baseScenario("fallback-economics", ModeNonStreaming)
	scenario.Circuit.FailureThreshold = 10
	scenario.Warmup = nil
	scenario.Providers[0].Rates = ratesUSD(1, 1)
	scenario.Providers[1].Rates = ratesUSD(1, 1)
	scenario.Requests = []Request{{ID: "request-1", Conditions: map[string]ProviderCondition{
		"openai":    failedCompletion(100*time.Millisecond, provider.ErrorTimeout, 1_000_000, 0),
		"anthropic": completion(200*time.Millisecond, 2_000_000, 0),
	}}}

	run, err := runPolicy(scenario, StateCold, gateway.RoutingPolicyDeterministic)
	if err != nil {
		t.Fatalf("runPolicy() error = %v", err)
	}
	result := run.Result
	if result.FallbackRequests != 1 || result.AverageAttemptsPerRequest != 2 || value(result.P50LatencyMS) != 300 {
		t.Fatalf("fallback metrics = %+v", result)
	}
	if result.TotalInputTokens != 3_000_000 || result.TotalOutputTokens != 0 || result.EstimatedCostMicroUSD != 3_000_000 {
		t.Fatalf("total economics = %+v", result)
	}
	if result.FallbackInputTokens != 2_000_000 || result.FallbackEstimatedCostMicroUSD != 2_000_000 {
		t.Fatalf("fallback economics = %+v", result)
	}
	if result.EstimatedCostPerSuccessfulRequestMicroUSD == nil || *result.EstimatedCostPerSuccessfulRequestMicroUSD != 3_000_000 {
		t.Fatalf("cost per success = %v", result.EstimatedCostPerSuccessfulRequestMicroUSD)
	}
}

func TestCircuitSkipsAndRecoversWithVirtualTime(t *testing.T) {
	scenario := baseScenario("circuit", ModeNonStreaming)
	scenario.Circuit = gateway.CircuitConfig{FailureThreshold: 2, OpenDuration: 500 * time.Millisecond}
	scenario.InterRequestGap = 100 * time.Millisecond
	scenario.Warmup = nil
	healthy := map[string]ProviderCondition{
		"openai": completion(100*time.Millisecond, 10, 1), "anthropic": completion(100*time.Millisecond, 10, 1),
	}
	for i := 0; i < 5; i++ {
		conditions := cloneConditions(healthy)
		if i < 2 {
			conditions["openai"] = failedCompletion(100*time.Millisecond, provider.ErrorUnavailable, 10, 1)
		}
		scenario.Requests = append(scenario.Requests, Request{ID: string(rune('a' + i)), Conditions: conditions})
	}

	run, err := runPolicy(scenario, StateCold, gateway.RoutingPolicyDeterministic)
	if err != nil {
		t.Fatalf("runPolicy() error = %v", err)
	}
	if run.Result.CircuitSkips != 2 {
		t.Fatalf("circuit skips = %d, want 2", run.Result.CircuitSkips)
	}
	if run.Result.ProviderAttempts["openai"] != 3 || run.Trace[4].Attempts[0].Provider != "openai" {
		t.Fatalf("attempts=%v recovery trace=%+v", run.Result.ProviderAttempts, run.Trace[4])
	}
	if run.Result.TotalInputTokens != 70 || run.Result.TotalOutputTokens != 7 {
		t.Fatalf("circuit-skipped provider contributed usage: %+v", run.Result)
	}
}

func TestStreamingMetricsPreserveCommitmentAndTTFC(t *testing.T) {
	scenario := baseScenario("stream-metrics", ModeStreaming)
	scenario.Circuit.FailureThreshold = 10
	scenario.Warmup = nil
	scenario.Requests = []Request{
		{ID: "pre-commit", Conditions: map[string]ProviderCondition{
			"openai":    streamFailure(50*time.Millisecond, 100*time.Millisecond, provider.ErrorTimeout, FailureBeforeCommit, 10, 1),
			"anthropic": streamSuccess(50*time.Millisecond, 200*time.Millisecond, 10, 2),
		}},
		{ID: "post-commit", Conditions: map[string]ProviderCondition{
			"openai":    streamFailure(40*time.Millisecond, 100*time.Millisecond, provider.ErrorUnavailable, FailureAfterCommit, 10, 1),
			"anthropic": streamSuccess(50*time.Millisecond, 200*time.Millisecond, 10, 2),
		}},
	}

	run, err := runPolicy(scenario, StateCold, gateway.RoutingPolicyDeterministic)
	if err != nil {
		t.Fatalf("runPolicy() error = %v", err)
	}
	result := run.Result
	if result.SuccessfulRequests != 1 || result.FailedRequests != 1 || result.FallbackRequests != 1 || result.PostCommitStreamFailures != 1 {
		t.Fatalf("stream outcomes = %+v", result)
	}
	if value(result.P50TTFCMS) != 40 || value(result.P95TTFCMS) != 150 {
		t.Fatalf("TTFC p50/p95 = %v/%v", result.P50TTFCMS, result.P95TTFCMS)
	}
	if value(result.P50StreamDurationMS) != 100 || value(result.P95StreamDurationMS) != 300 {
		t.Fatalf("duration p50/p95 = %v/%v", result.P50StreamDurationMS, result.P95StreamDurationMS)
	}
	if len(run.Trace[1].Attempts) != 1 {
		t.Fatal("post-commit failure incorrectly fell back")
	}
}

func TestColdStartExplorationAndWarmStateAreReportedSeparately(t *testing.T) {
	scenario := coldStartScenario()
	cold, err := RunComparison(scenario, StateCold, []string{gateway.RoutingPolicyDeterministic, gateway.RoutingPolicyLatency})
	if err != nil {
		t.Fatalf("cold RunComparison() error = %v", err)
	}
	warm, err := RunComparison(scenario, StateWarm, []string{gateway.RoutingPolicyLatency})
	if err != nil {
		t.Fatalf("warm RunComparison() error = %v", err)
	}
	if cold.Results[0].ExplorationSelections != 0 || cold.Results[1].ExplorationSelections == 0 {
		t.Fatalf("cold exploration = deterministic %d latency %d", cold.Results[0].ExplorationSelections, cold.Results[1].ExplorationSelections)
	}
	if reflect.DeepEqual(cold.Results[1].InitialProviderSelections, warm.Results[0].InitialProviderSelections) {
		t.Fatal("cold and warm state selection distributions unexpectedly match")
	}
}

func TestNearestRankPercentiles(t *testing.T) {
	values := []time.Duration{4 * time.Millisecond, time.Millisecond, 3 * time.Millisecond, 2 * time.Millisecond}
	if got := value(percentileMilliseconds(values, 50)); got != 2 {
		t.Fatalf("p50 = %d, want 2", got)
	}
	if got := value(percentileMilliseconds(values, 95)); got != 4 {
		t.Fatalf("p95 = %d, want 4", got)
	}
	if percentileMilliseconds(nil, 50) != nil {
		t.Fatal("empty percentile should be unavailable")
	}
}

func TestAllBuiltInScenariosAndPoliciesRunOffline(t *testing.T) {
	for _, name := range BuiltInScenarioNames {
		scenario, err := BuiltInScenario(name)
		if err != nil {
			t.Fatalf("BuiltInScenario(%s) error = %v", name, err)
		}
		comparison, err := RunComparison(scenario, StateWarm, nil)
		if err != nil {
			t.Fatalf("RunComparison(%s) error = %v", name, err)
		}
		if len(comparison.Results) != len(SupportedPolicies) {
			t.Fatalf("%s policies = %d", name, len(comparison.Results))
		}
	}
}

func cloneRequests(requests []Request) []Request {
	cloned := make([]Request, len(requests))
	for i, request := range requests {
		cloned[i] = Request{ID: request.ID, Conditions: cloneConditions(request.Conditions)}
	}
	return cloned
}

func value(value *int64) int64 {
	if value == nil {
		return -1
	}
	return *value
}

func TestMissingUsageRemainsUnavailableDuringFallback(t *testing.T) {
	scenario := baseScenario("missing-usage", ModeNonStreaming)
	scenario.Circuit.FailureThreshold = 10
	scenario.Warmup = nil
	scenario.Requests = []Request{{ID: "request", Conditions: map[string]ProviderCondition{
		"openai": {CompletionLatency: time.Millisecond, ErrorKind: provider.ErrorTimeout},
		"anthropic": {
			CompletionLatency: time.Millisecond,
			Usage:             &openai.Usage{},
		},
	}}}
	run, err := runPolicy(scenario, StateCold, gateway.RoutingPolicyDeterministic)
	if err != nil {
		t.Fatalf("runPolicy() error = %v", err)
	}
	if run.Result.FallbackAttemptsWithoutEstimatedCost != 1 || run.Result.EstimatedCostMicroUSD != 0 {
		t.Fatalf("missing usage economics = %+v", run.Result)
	}
}

func TestSimulatedProviderUsesExistingFailureTaxonomy(t *testing.T) {
	for _, test := range []struct {
		name         string
		condition    ProviderCondition
		wantAttempts int
	}{
		{name: "timeout", condition: ProviderCondition{CompletionLatency: time.Millisecond, ErrorKind: provider.ErrorTimeout}, wantAttempts: 2},
		{name: "unavailable", condition: ProviderCondition{CompletionLatency: time.Millisecond, ErrorKind: provider.ErrorUnavailable}, wantAttempts: 2},
		{name: "rate limited", condition: ProviderCondition{CompletionLatency: time.Millisecond, ErrorKind: provider.ErrorRateLimited}, wantAttempts: 2},
		{name: "invalid request", condition: ProviderCondition{CompletionLatency: time.Millisecond, ErrorKind: provider.ErrorInvalidRequest}, wantAttempts: 1},
		{name: "canceled", condition: ProviderCondition{CompletionLatency: time.Millisecond, Canceled: true}, wantAttempts: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			scenario := baseScenario("taxonomy", ModeNonStreaming)
			scenario.Circuit.FailureThreshold = 10
			scenario.Warmup = nil
			scenario.Requests = []Request{{ID: "request", Conditions: map[string]ProviderCondition{
				"openai": test.condition, "anthropic": completion(time.Millisecond, 1, 1),
			}}}
			run, err := runPolicy(scenario, StateCold, gateway.RoutingPolicyDeterministic)
			if err != nil {
				t.Fatalf("runPolicy() error = %v", err)
			}
			if len(run.Trace[0].Attempts) != test.wantAttempts {
				t.Fatalf("attempts = %d, want %d", len(run.Trace[0].Attempts), test.wantAttempts)
			}
		})
	}
}
