package accounting

import (
	"math"
	"sort"
	"sync"

	"github.com/VincentSh1/RouteForge/internal/openai"
)

const DefaultModelCapacity = 128

type ModelSnapshot struct {
	Provider                     string
	Model                        string
	Attempts                     uint64
	AttemptsWithUsage            uint64
	AttemptsWithoutUsage         uint64
	AttemptsWithPartialUsage     uint64
	InputTokens                  uint64
	OutputTokens                 uint64
	TotalTokens                  uint64
	AttemptsWithEstimatedCost    uint64
	AttemptsWithoutEstimatedCost uint64
	EstimatedCostMicroUSD        uint64
	Overflowed                   bool
}

type Snapshot struct {
	Models          []ModelSnapshot
	DroppedAttempts uint64
}

type RecordResult struct {
	EstimatedCostMicroUSD  uint64
	EstimatedCostAvailable bool
}

type Tracker struct {
	mu        sync.Mutex
	models    map[Key]*modelAggregate
	prices    PriceBook
	maxModels int
	dropped   uint64
}

type modelAggregate struct {
	attempts                     uint64
	attemptsWithUsage            uint64
	attemptsWithoutUsage         uint64
	attemptsWithPartialUsage     uint64
	inputTokens                  uint64
	outputTokens                 uint64
	totalTokens                  uint64
	attemptsWithEstimatedCost    uint64
	attemptsWithoutEstimatedCost uint64
	estimatedCostMicroUSD        uint64
	overflowed                   bool
}

func NewTracker(prices PriceBook, maxModels int) *Tracker {
	return &Tracker{
		models:    make(map[Key]*modelAggregate),
		prices:    ClonePriceBook(prices),
		maxModels: maxModels,
	}
}

func (t *Tracker) SetPrices(prices PriceBook) {
	t.mu.Lock()
	t.prices = ClonePriceBook(prices)
	t.mu.Unlock()
}

func (t *Tracker) Record(providerName, modelName string, providerUsage *openai.Usage) RecordResult {
	t.mu.Lock()
	defer t.mu.Unlock()

	key := Key{Provider: providerName, Model: modelName}
	estimatedCost, costAvailable := EstimateMicroUSD(providerUsage, t.prices[key])
	result := RecordResult{EstimatedCostMicroUSD: estimatedCost, EstimatedCostAvailable: costAvailable}
	aggregate := t.models[key]
	if aggregate == nil {
		if len(t.models) >= t.maxModels {
			t.dropped = saturatingAdd(t.dropped, 1, nil)
			return result
		}
		aggregate = &modelAggregate{}
		t.models[key] = aggregate
	}
	aggregate.attempts = saturatingAdd(aggregate.attempts, 1, &aggregate.overflowed)

	usageAvailable, complete := recordUsage(aggregate, providerUsage)
	if !usageAvailable {
		aggregate.attemptsWithoutUsage = saturatingAdd(aggregate.attemptsWithoutUsage, 1, &aggregate.overflowed)
	} else {
		aggregate.attemptsWithUsage = saturatingAdd(aggregate.attemptsWithUsage, 1, &aggregate.overflowed)
		if !complete {
			aggregate.attemptsWithPartialUsage = saturatingAdd(aggregate.attemptsWithPartialUsage, 1, &aggregate.overflowed)
		}
	}

	if !costAvailable {
		aggregate.attemptsWithoutEstimatedCost = saturatingAdd(aggregate.attemptsWithoutEstimatedCost, 1, &aggregate.overflowed)
		return result
	}
	aggregate.attemptsWithEstimatedCost = saturatingAdd(aggregate.attemptsWithEstimatedCost, 1, &aggregate.overflowed)
	aggregate.estimatedCostMicroUSD = saturatingAdd(aggregate.estimatedCostMicroUSD, estimatedCost, &aggregate.overflowed)
	return result
}

func recordUsage(aggregate *modelAggregate, providerUsage *openai.Usage) (available, complete bool) {
	if providerUsage == nil {
		return false, false
	}
	available = providerUsage.InputTokens != nil || providerUsage.OutputTokens != nil || providerUsage.TotalTokens != nil
	if !available {
		return false, false
	}
	if providerUsage.InputTokens != nil {
		aggregate.inputTokens = saturatingAdd(aggregate.inputTokens, *providerUsage.InputTokens, &aggregate.overflowed)
	}
	if providerUsage.OutputTokens != nil {
		aggregate.outputTokens = saturatingAdd(aggregate.outputTokens, *providerUsage.OutputTokens, &aggregate.overflowed)
	}
	if providerUsage.TotalTokens != nil {
		aggregate.totalTokens = saturatingAdd(aggregate.totalTokens, *providerUsage.TotalTokens, &aggregate.overflowed)
	}
	if providerUsage.InputTokens == nil || providerUsage.OutputTokens == nil || providerUsage.TotalTokens == nil {
		return true, false
	}
	input, output := *providerUsage.InputTokens, *providerUsage.OutputTokens
	return true, input <= math.MaxUint64-output && *providerUsage.TotalTokens == input+output
}

func (t *Tracker) Snapshot() Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()

	snapshot := Snapshot{
		Models:          make([]ModelSnapshot, 0, len(t.models)),
		DroppedAttempts: t.dropped,
	}
	for key, aggregate := range t.models {
		snapshot.Models = append(snapshot.Models, ModelSnapshot{
			Provider: key.Provider, Model: key.Model,
			Attempts:                 aggregate.attempts,
			AttemptsWithUsage:        aggregate.attemptsWithUsage,
			AttemptsWithoutUsage:     aggregate.attemptsWithoutUsage,
			AttemptsWithPartialUsage: aggregate.attemptsWithPartialUsage,
			InputTokens:              aggregate.inputTokens, OutputTokens: aggregate.outputTokens, TotalTokens: aggregate.totalTokens,
			AttemptsWithEstimatedCost:    aggregate.attemptsWithEstimatedCost,
			AttemptsWithoutEstimatedCost: aggregate.attemptsWithoutEstimatedCost,
			EstimatedCostMicroUSD:        aggregate.estimatedCostMicroUSD,
			Overflowed:                   aggregate.overflowed,
		})
	}
	sort.Slice(snapshot.Models, func(i, j int) bool {
		if snapshot.Models[i].Provider == snapshot.Models[j].Provider {
			return snapshot.Models[i].Model < snapshot.Models[j].Model
		}
		return snapshot.Models[i].Provider < snapshot.Models[j].Provider
	})
	return snapshot
}

func saturatingAdd(current, value uint64, overflowed *bool) uint64 {
	if current > math.MaxUint64-value {
		if overflowed != nil {
			*overflowed = true
		}
		return math.MaxUint64
	}
	return current + value
}
