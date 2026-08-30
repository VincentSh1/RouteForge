package accounting

import (
	"sync"
	"testing"

	"github.com/VincentSh1/RouteForge/internal/openai"
)

func TestTrackerAggregatesProviderModelsIndependently(t *testing.T) {
	inputRate, outputRate := uint64(1_000_000), uint64(2_000_000)
	prices := PriceBook{
		{Provider: "openai", Model: "model-a"}: {
			InputMicroUSDPerMillion: &inputRate, OutputMicroUSDPerMillion: &outputRate,
		},
	}
	tracker := NewTracker(prices, 4)
	tracker.Record("openai", "model-a", openai.NewUsage(2, 3, 5))
	tracker.Record("openai", "model-a", openai.NewUsage(4, 5, 9))
	tracker.Record("anthropic", "model-b", nil)

	snapshot := tracker.Snapshot()
	if len(snapshot.Models) != 2 {
		t.Fatalf("models = %+v", snapshot.Models)
	}
	openAI := snapshot.Models[1]
	if openAI.Provider != "openai" || openAI.Model != "model-a" || openAI.Attempts != 2 ||
		openAI.AttemptsWithUsage != 2 || openAI.InputTokens != 6 || openAI.OutputTokens != 8 || openAI.TotalTokens != 14 ||
		openAI.AttemptsWithEstimatedCost != 2 || openAI.AttemptsWithoutEstimatedCost != 0 {
		t.Fatalf("OpenAI aggregate = %+v", openAI)
	}
	anthropic := snapshot.Models[0]
	if anthropic.Attempts != 1 || anthropic.AttemptsWithoutUsage != 1 || anthropic.AttemptsWithoutEstimatedCost != 1 {
		t.Fatalf("Anthropic aggregate = %+v", anthropic)
	}
}

func TestTrackerRecordsPartialUsageWithoutFabricatingCost(t *testing.T) {
	input := uint64(7)
	tracker := NewTracker(nil, 2)
	tracker.Record("anthropic", "model", &openai.Usage{InputTokens: &input})
	snapshot := tracker.Snapshot().Models[0]
	if snapshot.AttemptsWithUsage != 1 || snapshot.AttemptsWithPartialUsage != 1 || snapshot.InputTokens != 7 ||
		snapshot.OutputTokens != 0 || snapshot.AttemptsWithoutEstimatedCost != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestTrackerSnapshotIsImmutableAndCapacityIsBounded(t *testing.T) {
	tracker := NewTracker(nil, 1)
	tracker.Record("first", "model", openai.NewUsage(1, 2, 3))
	tracker.Record("second", "model", openai.NewUsage(4, 5, 9))
	snapshot := tracker.Snapshot()
	if len(snapshot.Models) != 1 || snapshot.DroppedAttempts != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	snapshot.Models[0].Attempts = 99
	if got := tracker.Snapshot().Models[0].Attempts; got != 1 {
		t.Fatalf("tracker mutated through snapshot: %d", got)
	}
}

func TestTrackerConcurrentRecordingAndSnapshots(t *testing.T) {
	tracker := NewTracker(nil, 2)
	const workers = 100
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			tracker.Record("provider", "model", openai.NewUsage(1, 2, 3))
			_ = tracker.Snapshot()
		}()
	}
	wait.Wait()
	snapshot := tracker.Snapshot().Models[0]
	if snapshot.Attempts != workers || snapshot.InputTokens != workers || snapshot.TotalTokens != workers*3 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestTrackerAccumulatesRoundedAttemptCostsWithoutFloatDrift(t *testing.T) {
	inputRate, outputRate := uint64(1), uint64(0)
	tracker := NewTracker(PriceBook{{Provider: "provider", Model: "model"}: {
		InputMicroUSDPerMillion: &inputRate, OutputMicroUSDPerMillion: &outputRate,
	}}, 1)
	for range 10 {
		tracker.Record("provider", "model", openai.NewUsage(500_000, 0, 500_000))
	}
	snapshot := tracker.Snapshot().Models[0]
	if snapshot.EstimatedCostMicroUSD != 10 {
		t.Fatalf("cost = %d, want 10", snapshot.EstimatedCostMicroUSD)
	}
}
