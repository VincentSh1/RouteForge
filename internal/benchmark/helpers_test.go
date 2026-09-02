package benchmark

import (
	"fmt"
	"time"

	"github.com/VincentSh1/RouteForge/internal/accounting"
	"github.com/VincentSh1/RouteForge/internal/gateway"
	"github.com/VincentSh1/RouteForge/internal/openai"
	"github.com/VincentSh1/RouteForge/internal/provider"
)

func baseScenario(name string, mode Mode) Scenario {
	percent := uint64(20)
	return Scenario{
		Version: 1,
		Name:    name,
		Mode:    mode,
		Providers: []ProviderSpec{
			{Name: "openai", Model: "benchmark-openai", Rates: ratesUSD(2, 8)},
			{Name: "anthropic", Model: "benchmark-anthropic", Rates: ratesUSD(5, 15)},
		},
		InterRequestGap: 100 * time.Millisecond,
		Circuit:         gateway.CircuitConfig{FailureThreshold: 2, OpenDuration: 900 * time.Millisecond},
		Routing: gateway.RoutingConfig{
			MinSamples: 3, SampleMaxAge: 10 * time.Second, ExplorationInterval: 2,
			MaxLatencyOverFastestPercent: &percent,
		},
	}
}

func completion(latency time.Duration, input, output uint64) ProviderCondition {
	return ProviderCondition{CompletionLatency: latency, Usage: openai.NewUsage(input, output, input+output)}
}

func failedCompletion(latency time.Duration, kind provider.ErrorKind, input, output uint64) ProviderCondition {
	condition := completion(latency, input, output)
	condition.ErrorKind = kind
	return condition
}

func streamSuccess(ttfc, duration time.Duration, input, output uint64) ProviderCondition {
	return ProviderCondition{TTFC: ttfc, StreamDuration: duration, Usage: openai.NewUsage(input, output, input+output)}
}

func streamFailure(ttfc, duration time.Duration, kind provider.ErrorKind, point StreamFailurePoint, input, output uint64) ProviderCondition {
	condition := streamSuccess(ttfc, duration, input, output)
	condition.ErrorKind = kind
	condition.StreamFailure = point
	return condition
}

func repeatedRequests(prefix string, count int, conditions map[string]ProviderCondition) []Request {
	requests := make([]Request, count)
	for i := range requests {
		requests[i] = Request{ID: fmt.Sprintf("%s-%02d", prefix, i+1), Conditions: cloneConditions(conditions)}
	}
	return requests
}

func cloneConditions(conditions map[string]ProviderCondition) map[string]ProviderCondition {
	cloned := make(map[string]ProviderCondition, len(conditions))
	for providerName, condition := range conditions {
		cloned[providerName] = cloneCondition(condition)
	}
	return cloned
}

func ratesUSD(input, output uint64) accounting.Rates {
	return accounting.Rates{
		InputMicroUSDPerMillion:  uint64Pointer(input * accounting.MicroUSDPerUSD),
		OutputMicroUSDPerMillion: uint64Pointer(output * accounting.MicroUSDPerUSD),
	}
}

func uint64Pointer(value uint64) *uint64 { return &value }
