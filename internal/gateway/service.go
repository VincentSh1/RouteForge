package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/VincentSh1/RouteForge/internal/model"
	"github.com/VincentSh1/RouteForge/internal/openai"
	"github.com/VincentSh1/RouteForge/internal/provider"
)

var (
	ErrModelRequired        = errors.New("model is required")
	ErrMessagesRequired     = errors.New("at least one message is required")
	ErrStreamingUnsupported = errors.New("streaming is not supported")
	ErrNoProviders          = errors.New("no providers are configured")
	ErrNativeModelInAuto    = errors.New("provider-native models require explicit provider selection")
	ErrNoUsableModelMapping = errors.New("logical model is unavailable for configured providers")
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
	resolver  *model.Resolver
	fallback  bool
}

func New(p provider.Provider, resolver *model.Resolver) *Service {
	if resolver == nil {
		resolver = model.New(nil)
	}
	return &Service{providers: []provider.Provider{p}, resolver: resolver}
}

func NewAuto(resolver *model.Resolver, providers ...provider.Provider) *Service {
	if resolver == nil {
		resolver = model.New(nil)
	}
	return &Service{providers: providers, resolver: resolver, fallback: true}
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
	logicalModel := model.IsLogical(req.Model)
	if s.fallback && !logicalModel {
		return openai.ChatCompletionResponse{}, ErrNativeModelInAuto
	}

	var lastErr error
	attempted := false
	for _, item := range s.providers {
		providerRequest := req
		if logicalModel {
			resolved, err := s.resolver.Resolve(req.Model, item.Name())
			if err != nil {
				if s.fallback && isMissingMapping(err) {
					continue
				}
				return openai.ChatCompletionResponse{}, err
			}
			providerRequest.Model = resolved
		}

		attempted = true
		response, err := item.Complete(ctx, providerRequest)
		if err == nil {
			if logicalModel {
				response.Model = req.Model
			}
			return response, nil
		}
		lastErr = err
		if !s.fallback || ctx.Err() != nil || !eligibleForFallback(err) {
			return openai.ChatCompletionResponse{}, err
		}
	}
	if !attempted {
		return openai.ChatCompletionResponse{}, ErrNoUsableModelMapping
	}
	return openai.ChatCompletionResponse{}, lastErr
}

func isMissingMapping(err error) bool {
	var resolutionErr *model.ResolutionError
	return errors.As(err, &resolutionErr) && resolutionErr.Kind == model.ErrorMissingMapping
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
