package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	benchmarkpkg "github.com/VincentSh1/RouteForge/internal/benchmark"
)

type output struct {
	Comparisons []benchmarkpkg.Comparison `json:"comparisons"`
}

func main() {
	scenarioName := flag.String("scenario", "stable", "built-in scenario name or all")
	stateName := flag.String("state", string(benchmarkpkg.StateWarm), "benchmark state: cold or warm")
	policyNames := flag.String("policies", strings.Join(benchmarkpkg.SupportedPolicies, ","), "comma-separated routing policies")
	pretty := flag.Bool("pretty", true, "indent JSON output")
	flag.Parse()

	result, err := run(*scenarioName, benchmarkpkg.State(*stateName), splitPolicies(*policyNames))
	if err != nil {
		fmt.Fprintln(os.Stderr, "routeforge-bench:", err)
		os.Exit(1)
	}

	encoder := json.NewEncoder(os.Stdout)
	if *pretty {
		encoder.SetIndent("", "  ")
	}
	if err := encoder.Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, "routeforge-bench: encode report:", err)
		os.Exit(1)
	}
}

func run(scenarioName string, state benchmarkpkg.State, policies []string) (output, error) {
	names := []string{scenarioName}
	if scenarioName == "all" {
		names = benchmarkpkg.BuiltInScenarioNames
	}

	report := output{Comparisons: make([]benchmarkpkg.Comparison, 0, len(names))}
	for _, name := range names {
		scenario, err := benchmarkpkg.BuiltInScenario(name)
		if err != nil {
			return output{}, err
		}
		comparison, err := benchmarkpkg.RunComparison(scenario, state, policies)
		if err != nil {
			return output{}, err
		}
		report.Comparisons = append(report.Comparisons, comparison)
	}
	return report, nil
}

func splitPolicies(value string) []string {
	parts := strings.Split(value, ",")
	policies := make([]string, 0, len(parts))
	for _, part := range parts {
		if policy := strings.TrimSpace(part); policy != "" {
			policies = append(policies, policy)
		}
	}
	return policies
}
