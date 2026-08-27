package gateway

import (
	"context"
	"errors"

	"github.com/VincentSh1/RouteForge/internal/provider"
)

type providerOutcome uint8

const (
	outcomeSuccess providerOutcome = iota
	outcomeCanceled
	outcomeTimeout
	outcomeUnavailable
	outcomeRateLimited
	outcomeInvalidRequest
	outcomeInternal
	outcomeOtherFailure
)

func classifyProviderOutcome(ctx context.Context, err error) providerOutcome {
	if err == nil {
		return outcomeSuccess
	}
	if ctx != nil && ctx.Err() != nil {
		return outcomeCanceled
	}
	var providerErr *provider.Error
	if errors.As(err, &providerErr) {
		switch providerErr.Kind {
		case provider.ErrorTimeout:
			return outcomeTimeout
		case provider.ErrorUnavailable:
			return outcomeUnavailable
		case provider.ErrorRateLimited:
			return outcomeRateLimited
		case provider.ErrorInvalidRequest:
			return outcomeInvalidRequest
		case provider.ErrorInternal:
			return outcomeInternal
		default:
			return outcomeOtherFailure
		}
	}
	if errors.Is(err, context.Canceled) {
		return outcomeCanceled
	}
	return outcomeOtherFailure
}

func (o providerOutcome) degradesHealth() bool {
	return o == outcomeTimeout || o == outcomeUnavailable || o == outcomeRateLimited
}
