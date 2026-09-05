package gateway

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/VincentSh1/RouteForge/internal/accounting"
	"github.com/VincentSh1/RouteForge/internal/openai"
	"github.com/VincentSh1/RouteForge/internal/persistence"
)

const (
	maxPersistedProviderLength = 64
	maxPersistedModelLength    = 256
)

type requestHistory struct {
	service     *Service
	ctx         context.Context
	record      persistence.RequestRecord
	lastOutcome providerOutcome
	hasAttempt  bool
}

func (s *Service) beginRequestHistory(ctx context.Context, request openai.ChatCompletionRequest) *requestHistory {
	if s.historyRecorder == nil || !s.historyRecorder.Enabled() {
		return nil
	}
	requestID, err := s.requestIDGenerator()
	if err != nil {
		s.metrics.RecordPersistence(ctx, string(persistence.OutcomeWriteError))
		return nil
	}
	return &requestHistory{
		service: s,
		ctx:     ctx,
		record: persistence.RequestRecord{
			RequestID: requestID, StartedAt: s.now().UTC(), RoutingPolicy: boundedMetadata(s.routingName, 32),
			Streaming: request.Stream, LogicalModel: boundedMetadata(request.Model, maxPersistedModelLength),
		},
	}
}

func (h *requestHistory) startAttempt(providerName, resolvedModel string) int {
	if h == nil {
		return -1
	}
	index := len(h.record.Attempts)
	h.record.Attempts = append(h.record.Attempts, persistence.AttemptRecord{
		AttemptNumber:         index + 1,
		Provider:              boundedMetadata(providerName, maxPersistedProviderLength),
		ResolvedProviderModel: boundedMetadata(resolvedModel, maxPersistedModelLength),
		Fallback:              index > 0,
		StartedAt:             h.service.now().UTC(),
	})
	if index == 0 {
		provider := h.record.Attempts[index].Provider
		h.record.InitialProvider = &provider
	}
	return index
}

func (h *requestHistory) finishAttempt(
	index int,
	outcome providerOutcome,
	duration time.Duration,
	ttfc *time.Duration,
	usage *openai.Usage,
	accountingResult accounting.RecordResult,
) {
	if h == nil || index < 0 || index >= len(h.record.Attempts) {
		return
	}
	attempt := &h.record.Attempts[index]
	attempt.CompletedAt = h.service.now().UTC()
	if attempt.CompletedAt.Before(attempt.StartedAt) {
		attempt.CompletedAt = attempt.StartedAt
	}
	attempt.DurationUS = nonNegativeMicroseconds(duration)
	if ttfc != nil {
		value := nonNegativeMicroseconds(*ttfc)
		attempt.TTFCUS = &value
	}
	attempt.Outcome = outcome.String()
	h.lastOutcome = outcome
	h.hasAttempt = true
	if usage != nil {
		attempt.InputTokens = cloneTokenCount(usage.InputTokens)
		attempt.OutputTokens = cloneTokenCount(usage.OutputTokens)
		attempt.TotalTokens = cloneTokenCount(usage.TotalTokens)
	}
	if accountingResult.EstimatedCostAvailable {
		cost := accountingResult.EstimatedCostMicroUSD
		attempt.EstimatedCostMicroUSD = &cost
	}
	if outcome == outcomeSuccess {
		provider := attempt.Provider
		h.record.FinalProvider = &provider
	}
}

func (h *requestHistory) finish(err error) {
	if h == nil {
		return
	}
	h.record.CompletedAt = h.service.now().UTC()
	if h.record.CompletedAt.Before(h.record.StartedAt) {
		h.record.CompletedAt = h.record.StartedAt
	}
	h.record.DurationUS = nonNegativeMicroseconds(h.record.CompletedAt.Sub(h.record.StartedAt))
	h.record.AttemptCount = len(h.record.Attempts)
	if h.record.AttemptCount > 0 {
		h.record.FallbackCount = h.record.AttemptCount - 1
	}
	h.record.Outcome = requestHistoryOutcome(h.ctx, err)
	if err != nil && h.hasAttempt && h.lastOutcome == outcomeCanceled {
		h.record.Outcome = h.lastOutcome.String()
	}
	if result := h.service.historyRecorder.Submit(h.record); result == persistence.SubmitClosed {
		h.service.metrics.RecordPersistence(h.ctx, string(persistence.OutcomeWriteError))
	}
}

func requestHistoryOutcome(ctx context.Context, err error) string {
	if err == nil {
		return outcomeSuccess.String()
	}
	if ctx != nil && ctx.Err() != nil {
		return outcomeCanceled.String()
	}
	if errors.Is(err, ErrModelRequired) || errors.Is(err, ErrMessagesRequired) ||
		errors.Is(err, ErrStreamingUnsupported) || errors.Is(err, ErrNativeModelInAuto) {
		return outcomeInvalidRequest.String()
	}
	var roleError *UnsupportedRoleError
	if errors.As(err, &roleError) {
		return outcomeInvalidRequest.String()
	}
	return classifyProviderOutcome(ctx, err).String()
}

func boundedMetadata(value string, maximum int) string {
	if utf8.RuneCountInString(value) <= maximum {
		return value
	}
	runes := []rune(value)
	return string(runes[:maximum])
}

func nonNegativeMicroseconds(duration time.Duration) int64 {
	if duration < 0 {
		return 0
	}
	return duration.Microseconds()
}
