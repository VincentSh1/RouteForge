package benchmark

import (
	"sort"
	"time"

	"github.com/VincentSh1/RouteForge/internal/accounting"
)

type Comparison struct {
	Scenario string   `json:"scenario"`
	State    State    `json:"state"`
	Results  []Result `json:"results"`
}

type Result struct {
	Policy                                    string            `json:"policy"`
	Mode                                      Mode              `json:"mode"`
	Requests                                  uint64            `json:"requests"`
	SuccessfulRequests                        uint64            `json:"successful_requests"`
	FailedRequests                            uint64            `json:"failed_requests"`
	SuccessRate                               float64           `json:"success_rate"`
	FallbackRequests                          uint64            `json:"fallback_requests"`
	FallbackRate                              float64           `json:"fallback_rate"`
	AverageAttemptsPerRequest                 float64           `json:"average_attempts_per_request"`
	CircuitSkips                              uint64            `json:"circuit_skips"`
	PostCommitStreamFailures                  uint64            `json:"post_commit_stream_failures"`
	P50LatencyMS                              *int64            `json:"p50_latency_ms,omitempty"`
	P95LatencyMS                              *int64            `json:"p95_latency_ms,omitempty"`
	P50TTFCMS                                 *int64            `json:"p50_ttfc_ms,omitempty"`
	P95TTFCMS                                 *int64            `json:"p95_ttfc_ms,omitempty"`
	P50StreamDurationMS                       *int64            `json:"p50_stream_duration_ms,omitempty"`
	P95StreamDurationMS                       *int64            `json:"p95_stream_duration_ms,omitempty"`
	TotalInputTokens                          uint64            `json:"total_input_tokens"`
	TotalOutputTokens                         uint64            `json:"total_output_tokens"`
	EstimatedCostMicroUSD                     uint64            `json:"estimated_cost_micro_usd"`
	EstimatedCostPerSuccessfulRequestMicroUSD *uint64           `json:"estimated_cost_per_successful_request_micro_usd,omitempty"`
	FallbackInputTokens                       uint64            `json:"fallback_input_tokens"`
	FallbackOutputTokens                      uint64            `json:"fallback_output_tokens"`
	FallbackEstimatedCostMicroUSD             uint64            `json:"fallback_estimated_cost_micro_usd"`
	FallbackAttemptsWithoutEstimatedCost      uint64            `json:"fallback_attempts_without_estimated_cost"`
	InitialProviderSelections                 map[string]uint64 `json:"initial_provider_selections"`
	ProviderAttempts                          map[string]uint64 `json:"provider_attempts"`
	FallbackProviderAttempts                  map[string]uint64 `json:"fallback_provider_attempts"`
	ExplorationSelections                     uint64            `json:"exploration_selections"`
	ExplorationRate                           float64           `json:"exploration_rate"`
	ProviderSelectionSwitches                 uint64            `json:"provider_selection_switches"`
}

type metricAccumulator struct {
	result          Result
	latencies       []time.Duration
	ttfcs           []time.Duration
	streamDurations []time.Duration
	attempts        uint64
	lastSelection   string
}

func newMetricAccumulator(policy string, mode Mode) *metricAccumulator {
	return &metricAccumulator{result: Result{
		Policy:                    policy,
		Mode:                      mode,
		InitialProviderSelections: make(map[string]uint64),
		ProviderAttempts:          make(map[string]uint64),
		FallbackProviderAttempts:  make(map[string]uint64),
	}}
}

func (m *metricAccumulator) finish(accountingBefore, accountingAfter accounting.Snapshot) Result {
	result := m.result
	if result.Requests != 0 {
		result.SuccessRate = float64(result.SuccessfulRequests) / float64(result.Requests)
		result.FallbackRate = float64(result.FallbackRequests) / float64(result.Requests)
		result.AverageAttemptsPerRequest = float64(m.attempts) / float64(result.Requests)
		result.ExplorationRate = float64(result.ExplorationSelections) / float64(result.Requests)
	}
	result.P50LatencyMS = percentileMilliseconds(m.latencies, 50)
	result.P95LatencyMS = percentileMilliseconds(m.latencies, 95)
	result.P50TTFCMS = percentileMilliseconds(m.ttfcs, 50)
	result.P95TTFCMS = percentileMilliseconds(m.ttfcs, 95)
	result.P50StreamDurationMS = percentileMilliseconds(m.streamDurations, 50)
	result.P95StreamDurationMS = percentileMilliseconds(m.streamDurations, 95)

	before := totalAccounting(accountingBefore)
	after := totalAccounting(accountingAfter)
	result.TotalInputTokens = after.inputTokens - before.inputTokens
	result.TotalOutputTokens = after.outputTokens - before.outputTokens
	result.EstimatedCostMicroUSD = after.cost - before.cost
	if result.SuccessfulRequests != 0 {
		cost := roundedQuotient(result.EstimatedCostMicroUSD, result.SuccessfulRequests)
		result.EstimatedCostPerSuccessfulRequestMicroUSD = &cost
	}
	return result
}

func percentileMilliseconds(values []time.Duration, percentile int) *int64 {
	if len(values) == 0 {
		return nil
	}
	ordered := append([]time.Duration(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	// Nearest-rank uses ceil(p*n/100), with ranks starting at one.
	rank := (percentile*len(ordered) + 99) / 100
	value := ordered[rank-1].Milliseconds()
	return &value
}

type accountingTotal struct {
	inputTokens  uint64
	outputTokens uint64
	cost         uint64
}

func totalAccounting(snapshot accounting.Snapshot) accountingTotal {
	var total accountingTotal
	for _, model := range snapshot.Models {
		total.inputTokens += model.InputTokens
		total.outputTokens += model.OutputTokens
		total.cost += model.EstimatedCostMicroUSD
	}
	return total
}

func roundedQuotient(numerator, denominator uint64) uint64 {
	quotient := numerator / denominator
	if numerator%denominator >= (denominator+1)/2 {
		quotient++
	}
	return quotient
}
