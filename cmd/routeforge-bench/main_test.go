package main

import (
	"testing"

	benchmarkpkg "github.com/VincentSh1/RouteForge/internal/benchmark"
)

func TestRunBuiltInComparison(t *testing.T) {
	report, err := run("stable", benchmarkpkg.StateCold, []string{"deterministic", "cost"})
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if len(report.Comparisons) != 1 || len(report.Comparisons[0].Results) != 2 {
		t.Fatalf("report = %+v", report)
	}
}

func TestSplitPolicies(t *testing.T) {
	policies := splitPolicies(" deterministic, latency ,cost_latency ")
	if len(policies) != 3 || policies[0] != "deterministic" || policies[2] != "cost_latency" {
		t.Fatalf("policies = %v", policies)
	}
}
