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
	scenarioName := flag.String("scenario", "", "built-in scenario name or all (default stable)")
	scenarioFile := flag.String("scenario-file", "", "path to an offline v1 JSON scenario")
	stateName := flag.String("state", string(benchmarkpkg.StateWarm), "benchmark state: cold or warm")
	policyName := flag.String("policy", "", "single routing policy")
	policyNames := flag.String("policies", "", "comma-separated routing policies (default all)")
	pretty := flag.Bool("pretty", true, "indent JSON output")
	flag.Parse()

	policies, err := selectPolicies(*policyName, *policyNames)
	if err != nil {
		fmt.Fprintln(os.Stderr, "routeforge-bench:", err)
		os.Exit(1)
	}
	result, err := run(*scenarioName, *scenarioFile, benchmarkpkg.State(*stateName), policies)
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

func run(scenarioName, scenarioFile string, state benchmarkpkg.State, policies []string) (output, error) {
	if scenarioName != "" && scenarioFile != "" {
		return output{}, fmt.Errorf("-scenario and -scenario-file cannot be used together")
	}
	if scenarioFile != "" {
		scenario, err := benchmarkpkg.LoadScenarioFile(scenarioFile)
		if err != nil {
			return output{}, err
		}
		comparison, err := benchmarkpkg.RunComparison(scenario, state, policies)
		if err != nil {
			return output{}, err
		}
		return output{Comparisons: []benchmarkpkg.Comparison{comparison}}, nil
	}
	if scenarioName == "" {
		scenarioName = "stable"
	}
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

func selectPolicies(single, multiple string) ([]string, error) {
	if strings.TrimSpace(single) != "" && strings.TrimSpace(multiple) != "" {
		return nil, fmt.Errorf("-policy and -policies cannot be used together")
	}
	if policy := strings.TrimSpace(single); policy != "" {
		return []string{policy}, nil
	}
	return splitPolicies(multiple), nil
}
