package gateway

import (
	"context"
	"time"

	"github.com/VincentSh1/RouteForge/internal/accounting"
	"github.com/VincentSh1/RouteForge/internal/openai"
)

func (s *Service) recordAttemptStart(ctx context.Context, providerName string, streaming bool, attemptNumber int, fallbackFrom string, fallbackReason providerOutcome) {
	if attemptNumber == 1 {
		s.metrics.RecordRoutingSelection(ctx, providerName, s.routingName, streaming)
		return
	}
	s.metrics.RecordFallback(ctx, fallbackFrom, providerName, fallbackReason.String())
}

func (s *Service) recordAttemptFinish(
	ctx context.Context,
	providerName string,
	streaming bool,
	attemptNumber int,
	outcome providerOutcome,
	duration time.Duration,
	usage *openai.Usage,
	accountingResult accounting.RecordResult,
) {
	s.metrics.RecordProviderAttempt(ctx, providerName, outcome.String(), streaming, attemptNumber > 1, duration)
	s.metrics.RecordUsage(ctx, providerName, usage)
	if accountingResult.EstimatedCostAvailable {
		s.metrics.RecordEstimatedCost(ctx, providerName, accountingResult.EstimatedCostMicroUSD)
	}
}
