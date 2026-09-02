package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	benchmarkfixtures "github.com/VincentSh1/RouteForge/benchmarks"
	benchmarkpkg "github.com/VincentSh1/RouteForge/internal/benchmark"
)

func TestRunBuiltInComparison(t *testing.T) {
	report, err := run("stable", "", benchmarkpkg.StateCold, []string{"deterministic", "cost"})
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if len(report.Comparisons) != 1 || len(report.Comparisons[0].Results) != 2 {
		t.Fatalf("report = %+v", report)
	}
}

func TestRunUsesStableScenarioByDefault(t *testing.T) {
	report, err := run("", "", benchmarkpkg.StateCold, []string{"deterministic"})
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if report.Comparisons[0].Scenario != "stable" || report.Comparisons[0].ScenarioVersion != 1 {
		t.Fatalf("comparison = %+v", report.Comparisons[0])
	}
}

func TestRunExplicitScenarioFile(t *testing.T) {
	data, err := benchmarkfixtures.FS.ReadFile("v1/stable.json")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "custom.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	report, err := run("", path, benchmarkpkg.StateWarm, []string{"latency"})
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if report.Comparisons[0].Scenario != "stable" || len(report.Comparisons[0].Results) != 1 {
		t.Fatalf("report = %+v", report)
	}
}

func TestRunRejectsAmbiguousOrInvalidScenarioFiles(t *testing.T) {
	if _, err := run("stable", "custom.json", benchmarkpkg.StateCold, nil); err == nil || !strings.Contains(err.Error(), "cannot be used together") {
		t.Fatalf("ambiguous error = %v", err)
	}
	if _, err := run("", "missing.json", benchmarkpkg.StateCold, nil); err == nil || !strings.Contains(err.Error(), "fixture not found") {
		t.Fatalf("missing error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "future.json")
	if err := os.WriteFile(path, []byte(`{"version":2}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := run("", path, benchmarkpkg.StateCold, nil); err == nil || !strings.Contains(err.Error(), "unsupported scenario version") {
		t.Fatalf("version error = %v", err)
	}
}

func TestSplitPolicies(t *testing.T) {
	policies := splitPolicies(" deterministic, latency ,cost_latency ")
	if len(policies) != 3 || policies[0] != "deterministic" || policies[2] != "cost_latency" {
		t.Fatalf("policies = %v", policies)
	}
}

func TestSelectPoliciesRejectsAmbiguousFlags(t *testing.T) {
	if _, err := selectPolicies("latency", "cost"); err == nil {
		t.Fatal("selectPolicies() error = nil")
	}
	policies, err := selectPolicies("latency", "")
	if err != nil || len(policies) != 1 || policies[0] != "latency" {
		t.Fatalf("policies=%v error=%v", policies, err)
	}
}
