package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

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
	ErrCircuitOpen          = errors.New("provider circuit is open")
)

const (
	defaultCircuitFailureThreshold = 3
	defaultCircuitOpenDuration     = 30 * time.Second
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
	health    *healthTracker
}

func New(p provider.Provider, resolver *model.Resolver) *Service {
	return NewWithCircuitBreaker(p, resolver, CircuitConfig{
		FailureThreshold: defaultCircuitFailureThreshold,
		OpenDuration:     defaultCircuitOpenDuration,
	})
}

func NewWithCircuitBreaker(p provider.Provider, resolver *model.Resolver, config CircuitConfig) *Service {
	if resolver == nil {
		resolver = model.New(nil)
	}
	providers := []provider.Provider{p}
	return &Service{providers: providers, resolver: resolver, health: trackerFor(providers, config)}
}

func NewAuto(resolver *model.Resolver, providers ...provider.Provider) *Service {
	return NewAutoWithCircuitBreaker(resolver, CircuitConfig{
		FailureThreshold: defaultCircuitFailureThreshold,
		OpenDuration:     defaultCircuitOpenDuration,
	}, providers...)
}

func NewAutoWithCircuitBreaker(resolver *model.Resolver, config CircuitConfig, providers ...provider.Provider) *Service {
	if resolver == nil {
		resolver = model.New(nil)
	}
	return &Service{providers: providers, resolver: resolver, fallback: true, health: trackerFor(providers, config)}
}

func (s *Service) Complete(ctx context.Context, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if err := validateCore(req); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if req.Stream {
		return openai.ChatCompletionResponse{}, ErrStreamingUnsupported
	}
	if err := validateRoles(req); err != nil {
		return openai.ChatCompletionResponse{}, err
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
	usableMapping := false
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
		usableMapping = true

		healthAttempt, allowed := s.health.begin(item.Name())
		if !allowed {
			lastErr = circuitOpenError(item.Name())
			if !s.fallback {
				return openai.ChatCompletionResponse{}, lastErr
			}
			continue
		}

		attempted = true
		response, err := item.Complete(ctx, providerRequest)
		recordHealthOutcome(healthAttempt, ctx, err)
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
	if !usableMapping {
		return openai.ChatCompletionResponse{}, ErrNoUsableModelMapping
	}
	if !attempted && lastErr != nil {
		return openai.ChatCompletionResponse{}, lastErr
	}
	return openai.ChatCompletionResponse{}, lastErr
}

type EmitFunc func(provider.StreamChunk) error

func (s *Service) Stream(ctx context.Context, req openai.ChatCompletionRequest, emit EmitFunc) error {
	if err := validateCore(req); err != nil {
		return err
	}
	if err := validateRoles(req); err != nil {
		return err
	}
	if len(s.providers) == 0 {
		return ErrNoProviders
	}
	logicalModel := model.IsLogical(req.Model)
	if s.fallback && !logicalModel {
		return ErrNativeModelInAuto
	}

	var lastErr error
	attempted := false
	usableMapping := false
	for _, item := range s.providers {
		providerRequest := req
		if logicalModel {
			resolved, err := s.resolver.Resolve(req.Model, item.Name())
			if err != nil {
				if s.fallback && isMissingMapping(err) {
					continue
				}
				return err
			}
			providerRequest.Model = resolved
		}
		usableMapping = true

		streamingProvider, ok := item.(provider.StreamingProvider)
		if !ok {
			return provider.NewError(provider.ErrorInternal, item.Name(), errors.New("streaming is unavailable"))
		}
		healthAttempt, allowed := s.health.begin(item.Name())
		if !allowed {
			lastErr = circuitOpenError(item.Name())
			if !s.fallback {
				return lastErr
			}
			continue
		}
		attempted = true
		stream, err := streamingProvider.Stream(ctx, providerRequest)
		if err != nil {
			recordHealthOutcome(healthAttempt, ctx, err)
			lastErr = err
			if !s.fallback || ctx.Err() != nil || !eligibleForFallback(err) {
				return err
			}
			continue
		}

		committed := false
		for {
			chunk, err := stream.Next()
			if err != nil {
				_ = stream.Close()
				if errors.Is(err, io.EOF) {
					healthAttempt.success()
					return nil
				}
				recordHealthOutcome(healthAttempt, ctx, err)
				lastErr = err
				if !committed && s.fallback && ctx.Err() == nil && eligibleForFallback(err) {
					break
				}
				return err
			}
			if logicalModel {
				chunk.Model = req.Model
			}
			if err := emit(chunk); err != nil {
				_ = stream.Close()
				healthAttempt.ignore()
				return err
			}
			committed = true
		}
	}
	if !usableMapping {
		return ErrNoUsableModelMapping
	}
	if !attempted && lastErr != nil {
		return lastErr
	}
	return lastErr
}

func trackerFor(providers []provider.Provider, config CircuitConfig) *healthTracker {
	names := make([]string, len(providers))
	for i, item := range providers {
		names[i] = item.Name()
	}
	return newHealthTracker(names, config, nil)
}

func circuitOpenError(providerName string) error {
	return provider.NewError(provider.ErrorUnavailable, providerName, ErrCircuitOpen)
}

func recordHealthOutcome(attempt *healthAttempt, ctx context.Context, err error) {
	if err == nil {
		attempt.success()
		return
	}
	if ctx.Err() != nil || !degradesProviderHealth(err) {
		attempt.ignore()
		return
	}
	attempt.failure()
}

func degradesProviderHealth(err error) bool {
	var providerErr *provider.Error
	if !errors.As(err, &providerErr) {
		return false
	}
	return providerErr.Kind == provider.ErrorUnavailable ||
		providerErr.Kind == provider.ErrorTimeout ||
		providerErr.Kind == provider.ErrorRateLimited
}

func validateCore(req openai.ChatCompletionRequest) error {
	if strings.TrimSpace(req.Model) == "" {
		return ErrModelRequired
	}
	if len(req.Messages) == 0 {
		return ErrMessagesRequired
	}
	return nil
}

func validateRoles(req openai.ChatCompletionRequest) error {
	for i, message := range req.Messages {
		switch message.Role {
		case "system", "user", "assistant":
		default:
			return &UnsupportedRoleError{Index: i, Role: message.Role}
		}
	}
	return nil
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
