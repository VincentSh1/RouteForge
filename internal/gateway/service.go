package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/VincentSh1/RouteForge/internal/openai"
	"github.com/VincentSh1/RouteForge/internal/provider"
)

var (
	ErrModelRequired        = errors.New("model is required")
	ErrMessagesRequired     = errors.New("at least one message is required")
	ErrStreamingUnsupported = errors.New("streaming is not supported")
	ErrNoProviders          = errors.New("no providers are configured")
)

type UnsupportedRoleError struct {
	Index int
	Role  string
}

func (e *UnsupportedRoleError) Error() string {
	return fmt.Sprintf("messages[%d].role %q is not supported", e.Index, e.Role)
}

type Service struct {
	providers []provider.Provider
	fallback  bool
}

func New(p provider.Provider) *Service {
	return &Service{providers: []provider.Provider{p}}
}

func NewAuto(providers ...provider.Provider) *Service {
	return &Service{providers: providers, fallback: true}
}

func (s *Service) Complete(ctx context.Context, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if strings.TrimSpace(req.Model) == "" {
		return openai.ChatCompletionResponse{}, ErrModelRequired
	}
	if len(req.Messages) == 0 {
		return openai.ChatCompletionResponse{}, ErrMessagesRequired
	}
	if req.Stream {
		return openai.ChatCompletionResponse{}, ErrStreamingUnsupported
	}
	for i, message := range req.Messages {
		switch message.Role {
		case "system", "user", "assistant":
		default:
			return openai.ChatCompletionResponse{}, &UnsupportedRoleError{Index: i, Role: message.Role}
		}
	}

	if len(s.providers) == 0 {
		return openai.ChatCompletionResponse{}, ErrNoProviders
	}

	var lastErr error
	for _, item := range s.providers {
		response, err := item.Complete(ctx, req)
		if err == nil {
			return response, nil
		}
		lastErr = err
		if !s.fallback || ctx.Err() != nil || !eligibleForFallback(err) {
			return openai.ChatCompletionResponse{}, err
		}
	}
	return openai.ChatCompletionResponse{}, lastErr
}

func eligibleForFallback(err error) bool {
	var providerErr *provider.Error
	if !errors.As(err, &providerErr) {
		return false
	}
	return providerErr.Kind == provider.ErrorUnavailable ||
		providerErr.Kind == provider.ErrorTimeout ||
		providerErr.Kind == provider.ErrorRateLimited
}
