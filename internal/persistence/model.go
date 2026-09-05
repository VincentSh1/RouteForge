package persistence

import (
	"crypto/rand"
	"encoding/base64"
	"time"
)

const requestIDPrefix = "rfreq_"

type RequestRecord struct {
	RequestID       string
	StartedAt       time.Time
	CompletedAt     time.Time
	RoutingPolicy   string
	Streaming       bool
	LogicalModel    string
	InitialProvider *string
	FinalProvider   *string
	Outcome         string
	AttemptCount    int
	FallbackCount   int
	DurationUS      int64
	Attempts        []AttemptRecord
}

type AttemptRecord struct {
	AttemptNumber         int
	Provider              string
	ResolvedProviderModel string
	Fallback              bool
	StartedAt             time.Time
	CompletedAt           time.Time
	DurationUS            int64
	TTFCUS                *int64
	Outcome               string
	InputTokens           *uint64
	OutputTokens          *uint64
	TotalTokens           *uint64
	EstimatedCostMicroUSD *uint64
}

type IDGenerator func() (string, error)

func NewRequestID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return requestIDPrefix + base64.RawURLEncoding.EncodeToString(random[:]), nil
}

func (r RequestRecord) Clone() RequestRecord {
	clone := r
	clone.InitialProvider = cloneString(r.InitialProvider)
	clone.FinalProvider = cloneString(r.FinalProvider)
	clone.Attempts = make([]AttemptRecord, len(r.Attempts))
	for i, attempt := range r.Attempts {
		clone.Attempts[i] = attempt
		clone.Attempts[i].TTFCUS = cloneInt64(attempt.TTFCUS)
		clone.Attempts[i].InputTokens = cloneUint64(attempt.InputTokens)
		clone.Attempts[i].OutputTokens = cloneUint64(attempt.OutputTokens)
		clone.Attempts[i].TotalTokens = cloneUint64(attempt.TotalTokens)
		clone.Attempts[i].EstimatedCostMicroUSD = cloneUint64(attempt.EstimatedCostMicroUSD)
	}
	return clone
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneUint64(value *uint64) *uint64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
