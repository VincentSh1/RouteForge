package benchmark

import (
	"fmt"
	"time"

	"github.com/VincentSh1/RouteForge/internal/accounting"
	"github.com/VincentSh1/RouteForge/internal/gateway"
	"github.com/VincentSh1/RouteForge/internal/openai"
	"github.com/VincentSh1/RouteForge/internal/provider"
)

var BuiltInScenarioNames = []string{"stable", "degradation", "rate_limit", "streaming", "cold_start"}

func BuiltInScenario(name string) (Scenario, error) {
	switch name {
	case "stable":
		return stableScenario(), nil
	case "degradation":
		return degradationScenario(), nil
	case "rate_limit":
		return rateLimitScenario(), nil
	case "streaming":
		return streamingScenario(), nil
	case "cold_start":
		return coldStartScenario(), nil
	default:
		return Scenario{}, fmt.Errorf("unknown built-in scenario %q", name)
	}
}

func baseScenario(name string, mode Mode) Scenario {
	percent := uint64(20)
	return Scenario{
		Name: name,
		Mode: mode,
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

func stableScenario() Scenario {
	scenario := baseScenario("stable", ModeNonStreaming)
	condition := map[string]ProviderCondition{
		"openai":    completion(300*time.Millisecond, 500, 100),
		"anthropic": completion(220*time.Millisecond, 520, 95),
	}
	scenario.Warmup = repeatedRequests("stable-warm", 8, condition)
	scenario.Requests = repeatedRequests("stable", 12, condition)
	return scenario
}

func degradationScenario() Scenario {
	scenario := baseScenario("degradation", ModeNonStreaming)
	healthy := map[string]ProviderCondition{
		"openai":    completion(180*time.Millisecond, 480, 90),
		"anthropic": completion(260*time.Millisecond, 500, 95),
	}
	scenario.Warmup = repeatedRequests("degradation-warm", 8, healthy)
	for i := 0; i < 14; i++ {
		conditions := cloneConditions(healthy)
		if i >= 3 && i <= 7 {
			conditions["openai"] = failedCompletion(450*time.Millisecond, provider.ErrorTimeout, 480, 20)
		}
		scenario.Requests = append(scenario.Requests, Request{ID: fmt.Sprintf("degradation-%02d", i+1), Conditions: conditions})
	}
	return scenario
}

func rateLimitScenario() Scenario {
	scenario := baseScenario("rate_limit", ModeNonStreaming)
	scenario.Providers[0].Rates = ratesUSD(1, 4)
	healthy := map[string]ProviderCondition{
		"openai":    completion(210*time.Millisecond, 450, 80),
		"anthropic": completion(280*time.Millisecond, 470, 85),
	}
	scenario.Warmup = repeatedRequests("rate-warm", 8, healthy)
	for i := 0; i < 12; i++ {
		conditions := cloneConditions(healthy)
		if i >= 2 && i <= 5 {
			conditions["openai"] = ProviderCondition{CompletionLatency: 80 * time.Millisecond, ErrorKind: provider.ErrorRateLimited}
		}
		scenario.Requests = append(scenario.Requests, Request{ID: fmt.Sprintf("rate-%02d", i+1), Conditions: conditions})
	}
	return scenario
}

func streamingScenario() Scenario {
	scenario := baseScenario("streaming", ModeStreaming)
	scenario.Routing.MaxLatencyOverFastestPercent = uint64Pointer(50)
	scenario.Providers[0].Rates = ratesUSD(5, 15)
	scenario.Providers[1].Rates = ratesUSD(2, 8)
	healthy := map[string]ProviderCondition{
		"openai":    streamSuccess(100*time.Millisecond, 700*time.Millisecond, 420, 110),
		"anthropic": streamSuccess(140*time.Millisecond, 620*time.Millisecond, 430, 105),
	}
	scenario.Warmup = repeatedRequests("stream-warm", 14, healthy)
	for i := 0; i < 12; i++ {
		conditions := cloneConditions(healthy)
		switch i {
		case 2:
			conditions["openai"] = streamFailure(90*time.Millisecond, 300*time.Millisecond, provider.ErrorTimeout, FailureBeforeCommit, 420, 15)
		case 5:
			conditions["openai"] = streamFailure(100*time.Millisecond, 500*time.Millisecond, provider.ErrorUnavailable, FailureAfterCommit, 420, 60)
		case 8:
			conditions["anthropic"] = streamFailure(140*time.Millisecond, 480*time.Millisecond, provider.ErrorRateLimited, FailureAfterCommit, 430, 55)
		}
		scenario.Requests = append(scenario.Requests, Request{ID: fmt.Sprintf("stream-%02d", i+1), Conditions: conditions})
	}
	return scenario
}

func coldStartScenario() Scenario {
	scenario := baseScenario("cold_start", ModeNonStreaming)
	scenario.Routing.MinSamples = 5
	scenario.Routing.ExplorationInterval = 3
	condition := map[string]ProviderCondition{
		"openai":    completion(300*time.Millisecond, 400, 80),
		"anthropic": completion(190*time.Millisecond, 410, 78),
	}
	scenario.Warmup = repeatedRequests("cold-warm", 18, condition)
	scenario.Requests = repeatedRequests("cold", 18, condition)
	return scenario
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
