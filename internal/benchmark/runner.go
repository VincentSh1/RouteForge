package benchmark

import (
	"context"
	"fmt"
	"time"

	"github.com/VincentSh1/RouteForge/internal/accounting"
	"github.com/VincentSh1/RouteForge/internal/gateway"
	"github.com/VincentSh1/RouteForge/internal/model"
	"github.com/VincentSh1/RouteForge/internal/openai"
	"github.com/VincentSh1/RouteForge/internal/provider"
)

var SupportedPolicies = []string{
	gateway.RoutingPolicyDeterministic,
	gateway.RoutingPolicyLatency,
	gateway.RoutingPolicyCost,
	gateway.RoutingPolicyCostLatency,
}

type requestTrace struct {
	ID       string
	Attempts []attemptRecord
	Success  bool
}

type policyRun struct {
	Result Result
	Trace  []requestTrace
}

func RunComparison(scenario Scenario, state State, policies []string) (Comparison, error) {
	if err := scenario.Validate(); err != nil {
		return Comparison{}, err
	}
	if state != StateCold && state != StateWarm {
		return Comparison{}, fmt.Errorf("benchmark state must be cold or warm")
	}
	if len(policies) == 0 {
		policies = SupportedPolicies
	}

	comparison := Comparison{Scenario: scenario.Name, State: state, Results: make([]Result, 0, len(policies))}
	for _, policy := range policies {
		if !supportedPolicy(policy) {
			return Comparison{}, fmt.Errorf("unsupported benchmark policy %q", policy)
		}
		run, err := runPolicy(scenario, state, policy)
		if err != nil {
			return Comparison{}, fmt.Errorf("run %s: %w", policy, err)
		}
		comparison.Results = append(comparison.Results, run.Result)
	}
	return comparison, nil
}

func runPolicy(scenario Scenario, state State, policy string) (policyRun, error) {
	clock := newVirtualClock()
	recorder := &attemptRecorder{}
	providers := make([]provider.Provider, 0, len(scenario.Providers))
	simulated := make(map[string]*simulatedProvider, len(scenario.Providers))
	mappings := make(map[string]string, len(scenario.Providers))
	prices := make(accounting.PriceBook, len(scenario.Providers))
	for _, spec := range scenario.Providers {
		item := &simulatedProvider{name: spec.Name, clock: clock, recorder: recorder}
		simulated[spec.Name] = item
		providers = append(providers, item)
		mappings[spec.Name] = spec.Model
		prices[accounting.Key{Provider: spec.Name, Model: spec.Model}] = spec.Rates
	}

	routingConfig := scenario.Routing
	routingConfig.Policy = policy
	service, err := gateway.NewAutoWithRoutingClock(
		model.New(map[string]map[string]string{model.General: mappings}),
		scenario.Circuit,
		routingConfig,
		clock,
		providers...,
	)
	if err != nil {
		return policyRun{}, err
	}
	service.SetPricing(prices)

	if state == StateWarm {
		for _, request := range scenario.Warmup {
			prepareProviders(simulated, request)
			recorder.reset()
			_, _, _ = executeRequest(service, scenario.Mode, clock)
			clock.Advance(scenario.InterRequestGap)
		}
	}

	accountingBefore := service.AccountingSnapshot()
	metrics := newMetricAccumulator(policy, scenario.Mode)
	trace := make([]requestTrace, 0, len(scenario.Requests))
	for _, request := range scenario.Requests {
		prepareProviders(simulated, request)
		recorder.reset()
		circuitSkips, explorationTarget := preRequestState(service, scenario, policy, clock.Now())
		started := clock.Now()
		success, firstContentAt, err := executeRequest(service, scenario.Mode, clock)
		finished := clock.Now()
		records := recorder.snapshot()

		metrics.result.Requests++
		metrics.result.CircuitSkips += circuitSkips
		metrics.attempts += uint64(len(records))
		if success {
			metrics.result.SuccessfulRequests++
		} else {
			metrics.result.FailedRequests++
		}
		if scenario.Mode == ModeNonStreaming {
			metrics.latencies = append(metrics.latencies, finished.Sub(started))
		} else {
			metrics.streamDurations = append(metrics.streamDurations, finished.Sub(started))
			if !firstContentAt.IsZero() {
				metrics.ttfcs = append(metrics.ttfcs, firstContentAt.Sub(started))
				if err != nil {
					metrics.result.PostCommitStreamFailures++
				}
			}
		}

		for index, record := range records {
			metrics.result.ProviderAttempts[record.Provider]++
			if index == 0 {
				metrics.result.InitialProviderSelections[record.Provider]++
				if metrics.lastSelection != "" && metrics.lastSelection != record.Provider {
					metrics.result.ProviderSelectionSwitches++
				}
				metrics.lastSelection = record.Provider
				if explorationTarget != "" && record.Provider == explorationTarget {
					metrics.result.ExplorationSelections++
				}
				continue
			}
			metrics.result.FallbackProviderAttempts[record.Provider]++
			addFallbackEconomics(&metrics.result, record, prices)
		}
		if len(records) > 1 {
			metrics.result.FallbackRequests++
		}

		trace = append(trace, requestTrace{ID: request.ID, Attempts: records, Success: success})
		clock.Advance(scenario.InterRequestGap)
	}

	metrics.result = metrics.finish(accountingBefore, service.AccountingSnapshot())
	return policyRun{Result: metrics.result, Trace: trace}, nil
}

func executeRequest(service *gateway.Service, mode Mode, clock *virtualClock) (bool, time.Time, error) {
	request := openai.ChatCompletionRequest{
		Model: model.General, Messages: []openai.Message{{Role: "user"}}, Stream: mode == ModeStreaming,
	}
	if mode == ModeNonStreaming {
		_, err := service.Complete(context.Background(), request)
		return err == nil, time.Time{}, err
	}

	var firstContentAt time.Time
	err := service.Stream(context.Background(), request, func(chunk provider.StreamChunk) error {
		if firstContentAt.IsZero() && chunk.Content != "" {
			firstContentAt = clock.Now()
		}
		return nil
	})
	return err == nil, firstContentAt, err
}

func prepareProviders(providers map[string]*simulatedProvider, request Request) {
	for providerName, item := range providers {
		item.prepare(request.Conditions[providerName])
	}
}

func preRequestState(service *gateway.Service, scenario Scenario, policy string, now time.Time) (uint64, string) {
	eligible := make([]ProviderSpec, 0, len(scenario.Providers))
	var skips uint64
	for _, spec := range scenario.Providers {
		if !service.ProviderEligible(spec.Name) {
			skips++
			continue
		}
		eligible = append(eligible, spec)
	}
	if policy != gateway.RoutingPolicyLatency && policy != gateway.RoutingPolicyCostLatency {
		return skips, ""
	}

	target := ""
	largestDeficit := 0
	for _, spec := range eligible {
		snapshot, _ := service.TelemetrySnapshot(spec.Name)
		fresh := freshSampleCount(snapshot, scenario.Mode, now, scenario.Routing.SampleMaxAge)
		if fresh >= scenario.Routing.MinSamples {
			continue
		}
		deficit := scenario.Routing.MinSamples - fresh
		if deficit > largestDeficit {
			largestDeficit = deficit
			target = spec.Name
		}
	}
	if target == "" || len(eligible) == 0 || target == eligible[0].Name {
		return skips, ""
	}
	return skips, target
}

func freshSampleCount(snapshot gateway.ProviderTelemetrySnapshot, mode Mode, now time.Time, maxAge time.Duration) int {
	samples := snapshot.NonStreamingLatencySamples
	if mode == ModeStreaming {
		samples = snapshot.StreamingFirstContentSamples
	}
	count := 0
	for _, sample := range samples {
		if !sample.ObservedAt.After(now) && now.Sub(sample.ObservedAt) <= maxAge {
			count++
		}
	}
	return count
}

func addFallbackEconomics(result *Result, record attemptRecord, prices accounting.PriceBook) {
	usage := record.Condition.Usage
	if usage != nil {
		if usage.InputTokens != nil {
			result.FallbackInputTokens += *usage.InputTokens
		}
		if usage.OutputTokens != nil {
			result.FallbackOutputTokens += *usage.OutputTokens
		}
	}
	cost, ok := accounting.EstimateMicroUSD(usage, prices[accounting.Key{Provider: record.Provider, Model: record.Model}])
	if !ok {
		result.FallbackAttemptsWithoutEstimatedCost++
		return
	}
	result.FallbackEstimatedCostMicroUSD += cost
}

func supportedPolicy(policy string) bool {
	for _, supported := range SupportedPolicies {
		if policy == supported {
			return true
		}
	}
	return false
}
