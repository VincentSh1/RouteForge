package adminapi

import (
	"time"

	"github.com/VincentSh1/RouteForge/internal/persistence"
)

// Explicit DTOs keep the public contract independent of future persistence fields.
type requestSummary struct {
	RequestID         string    `json:"request_id"`
	StartedAt         time.Time `json:"started_at"`
	CompletedAt       time.Time `json:"completed_at"`
	RoutingPolicy     string    `json:"routing_policy"`
	Streaming         bool      `json:"streaming"`
	LogicalModel      string    `json:"logical_model"`
	InitialProvider   *string   `json:"initial_provider"`
	FinalProvider     *string   `json:"final_provider"`
	Outcome           string    `json:"outcome"`
	AttemptCount      int       `json:"attempt_count"`
	FallbackCount     int       `json:"fallback_count"`
	RequestDurationUS int64     `json:"request_duration_us"`
	CacheHit          bool      `json:"cache_hit"`
}

type attemptSummary struct {
	AttemptNumber         int       `json:"attempt_number"`
	Provider              string    `json:"provider"`
	ResolvedProviderModel string    `json:"resolved_provider_model"`
	Fallback              bool      `json:"fallback"`
	StartedAt             time.Time `json:"started_at"`
	CompletedAt           time.Time `json:"completed_at"`
	DurationUS            int64     `json:"duration_us"`
	TTFCUS                *int64    `json:"ttfc_us"`
	Outcome               string    `json:"outcome"`
	InputTokens           *uint64   `json:"input_tokens"`
	OutputTokens          *uint64   `json:"output_tokens"`
	TotalTokens           *uint64   `json:"total_tokens"`
	EstimatedCostMicroUSD *uint64   `json:"estimated_cost_micro_usd"`
}

type requestDetail struct {
	requestSummary
	Attempts []attemptSummary `json:"attempts"`
}

func summary(record persistence.RequestRecord) requestSummary {
	return requestSummary{
		RequestID: record.RequestID, StartedAt: record.StartedAt.UTC(), CompletedAt: record.CompletedAt.UTC(),
		RoutingPolicy: record.RoutingPolicy, Streaming: record.Streaming, LogicalModel: record.LogicalModel,
		InitialProvider: record.InitialProvider, FinalProvider: record.FinalProvider, Outcome: record.Outcome,
		AttemptCount: record.AttemptCount, FallbackCount: record.FallbackCount, RequestDurationUS: record.DurationUS, CacheHit: record.CacheHit,
	}
}

func detail(record persistence.RequestRecord) requestDetail {
	result := requestDetail{requestSummary: summary(record), Attempts: make([]attemptSummary, 0, len(record.Attempts))}
	for _, a := range record.Attempts {
		result.Attempts = append(result.Attempts, attemptSummary{AttemptNumber: a.AttemptNumber, Provider: a.Provider,
			ResolvedProviderModel: a.ResolvedProviderModel, Fallback: a.Fallback, StartedAt: a.StartedAt.UTC(), CompletedAt: a.CompletedAt.UTC(),
			DurationUS: a.DurationUS, TTFCUS: a.TTFCUS, Outcome: a.Outcome, InputTokens: a.InputTokens, OutputTokens: a.OutputTokens,
			TotalTokens: a.TotalTokens, EstimatedCostMicroUSD: a.EstimatedCostMicroUSD})
	}
	return result
}
