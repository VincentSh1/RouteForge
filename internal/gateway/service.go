package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/VincentSh1/RouteForge/internal/accounting"
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
	providers    []provider.Provider
	resolver     *model.Resolver
	fallback     bool
	health       *healthTracker
	telemetry    *telemetryTracker
	routing      routingPolicy
	rankEligible bool
	now          func() time.Time
	accounting   *accounting.Tracker
	pricingMu    sync.RWMutex
	pricing      accounting.PriceBook
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
	return &Service{
		providers:  providers,
		resolver:   resolver,
		health:     trackerFor(providers, config),
		telemetry:  telemetryFor(providers),
		routing:    deterministicRoutingPolicy{},
		now:        time.Now,
		accounting: accounting.NewTracker(nil, accounting.DefaultModelCapacity),
	}
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
	return &Service{
		providers:  providers,
		resolver:   resolver,
		fallback:   true,
		health:     trackerFor(providers, config),
		telemetry:  telemetryFor(providers),
		routing:    deterministicRoutingPolicy{},
		now:        time.Now,
		accounting: accounting.NewTracker(nil, accounting.DefaultModelCapacity),
	}
}

func NewAutoWithRouting(resolver *model.Resolver, circuitConfig CircuitConfig, routingConfig RoutingConfig, providers ...provider.Provider) (*Service, error) {
	return newAutoService(resolver, circuitConfig, routingConfig, providers...)
}

func newAutoService(resolver *model.Resolver, circuitConfig CircuitConfig, routingConfig RoutingConfig, providers ...provider.Provider) (*Service, error) {
	if resolver == nil {
		resolver = model.New(nil)
	}
	routing, rankEligible, err := newRoutingPolicy(routingConfig)
	if err != nil {
		return nil, err
	}
	return &Service{
		providers:    providers,
		resolver:     resolver,
		fallback:     true,
		health:       trackerFor(providers, circuitConfig),
		telemetry:    telemetryFor(providers),
		routing:      routing,
		rankEligible: rankEligible,
		now:          time.Now,
		accounting:   accounting.NewTracker(nil, accounting.DefaultModelCapacity),
	}, nil
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
	for _, item := range s.orderedProviders(nonStreamingMode, req.Model) {
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
		telemetryAttempt := s.telemetry.begin(item.Name())
		response, err := item.Complete(ctx, providerRequest)
		s.accounting.Record(item.Name(), providerRequest.Model, response.Usage)
		outcome := classifyProviderOutcome(ctx, err)
		recordHealthOutcome(healthAttempt, outcome)
		telemetryAttempt.finishNonStreaming(outcome)
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
	for _, item := range s.orderedProviders(streamingMode, req.Model) {
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
		telemetryAttempt := s.telemetry.begin(item.Name())
		stream, err := streamingProvider.Stream(ctx, providerRequest)
		if err != nil {
			s.accounting.Record(item.Name(), providerRequest.Model, nil)
			outcome := classifyProviderOutcome(ctx, err)
			recordHealthOutcome(healthAttempt, outcome)
			telemetryAttempt.finishStreaming(outcome)
			lastErr = err
			if !s.fallback || ctx.Err() != nil || !eligibleForFallback(err) {
				return err
			}
			continue
		}

		committed := false
		var attemptUsage *openai.Usage
		for {
			chunk, err := stream.Next()
			if err != nil {
				_ = stream.Close()
				s.accounting.Record(item.Name(), providerRequest.Model, attemptUsage)
				if errors.Is(err, io.EOF) {
					healthAttempt.success()
					telemetryAttempt.finishStreaming(outcomeSuccess)
					return nil
				}
				outcome := classifyProviderOutcome(ctx, err)
				recordHealthOutcome(healthAttempt, outcome)
				telemetryAttempt.finishStreaming(outcome)
				lastErr = err
				if !committed && s.fallback && ctx.Err() == nil && eligibleForFallback(err) {
					break
				}
				return err
			}
			if logicalModel {
				chunk.Model = req.Model
			}
			attemptUsage = mergeUsage(attemptUsage, chunk.Usage)
			if !clientVisibleChunk(chunk) {
				continue
			}
			chunk.Usage = nil
			if chunk.Content != "" {
				telemetryAttempt.firstContent()
			}
			if err := emit(chunk); err != nil {
				_ = stream.Close()
				s.accounting.Record(item.Name(), providerRequest.Model, attemptUsage)
				healthAttempt.ignore()
				telemetryAttempt.finishStreaming(outcomeCanceled)
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
	return newHealthTracker(providerNames(providers), config, nil)
}

func telemetryFor(providers []provider.Provider) *telemetryTracker {
	return newTelemetryTracker(providerNames(providers), defaultTelemetrySampleCapacity, nil)
}

func providerNames(providers []provider.Provider) []string {
	names := make([]string, len(providers))
	for i, item := range providers {
		names[i] = item.Name()
	}
	return names
}

func (s *Service) orderedProviders(mode requestMode, requestModel string) []provider.Provider {
	if !s.fallback || !s.rankEligible {
		return s.routing.order(s.providers, mode, nil, nil, s.now())
	}

	eligible := make([]provider.Provider, 0, len(s.providers))
	ineligible := make([]provider.Provider, 0, len(s.providers))
	snapshots := make(map[string]ProviderTelemetrySnapshot, len(s.providers))
	for _, item := range s.providers {
		if !s.health.eligible(item.Name()) {
			ineligible = append(ineligible, item)
			continue
		}
		eligible = append(eligible, item)
		if snapshot, ok := s.telemetry.snapshot(item.Name()); ok {
			snapshots[item.Name()] = snapshot
		}
	}

	prices := s.candidatePrices(requestModel, eligible)
	ordered := s.routing.order(eligible, mode, snapshots, prices, s.now())
	return append(ordered, ineligible...)
}

func (s *Service) candidatePrices(requestModel string, candidates []provider.Provider) map[string]accounting.Rates {
	if _, ok := s.routing.(costRoutingPolicy); !ok {
		return nil
	}

	s.pricingMu.RLock()
	prices := accounting.ClonePriceBook(s.pricing)
	s.pricingMu.RUnlock()

	resolved := make(map[string]accounting.Rates, len(candidates))
	for _, item := range candidates {
		modelName := requestModel
		if model.IsLogical(requestModel) {
			var err error
			modelName, err = s.resolver.Resolve(requestModel, item.Name())
			if err != nil {
				continue
			}
		}
		if rates, ok := prices[accounting.Key{Provider: item.Name(), Model: modelName}]; ok {
			resolved[item.Name()] = rates
		}
	}
	return resolved
}

func circuitOpenError(providerName string) error {
	return provider.NewError(provider.ErrorUnavailable, providerName, ErrCircuitOpen)
}

func recordHealthOutcome(attempt *healthAttempt, outcome providerOutcome) {
	if outcome == outcomeSuccess {
		attempt.success()
		return
	}
	if !outcome.degradesHealth() {
		attempt.ignore()
		return
	}
	attempt.failure()
}

func degradesProviderHealth(err error) bool {
	return classifyProviderOutcome(nil, err).degradesHealth()
}

func (s *Service) TelemetrySnapshot(providerName string) (ProviderTelemetrySnapshot, bool) {
	return s.telemetry.snapshot(providerName)
}

func (s *Service) SetPricing(prices accounting.PriceBook) {
	s.pricingMu.Lock()
	s.pricing = accounting.ClonePriceBook(prices)
	s.pricingMu.Unlock()
	s.accounting.SetPrices(prices)
}

func (s *Service) AccountingSnapshot() accounting.Snapshot {
	return s.accounting.Snapshot()
}

func mergeUsage(current, incoming *openai.Usage) *openai.Usage {
	if incoming == nil {
		return current
	}
	merged := &openai.Usage{}
	if current != nil {
		merged.InputTokens = cloneTokenCount(current.InputTokens)
		merged.OutputTokens = cloneTokenCount(current.OutputTokens)
		merged.TotalTokens = cloneTokenCount(current.TotalTokens)
	}
	if incoming.InputTokens != nil {
		merged.InputTokens = cloneTokenCount(incoming.InputTokens)
	}
	if incoming.OutputTokens != nil {
		merged.OutputTokens = cloneTokenCount(incoming.OutputTokens)
	}
	if incoming.TotalTokens != nil {
		merged.TotalTokens = cloneTokenCount(incoming.TotalTokens)
	}
	return merged
}

func cloneTokenCount(value *uint64) *uint64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func clientVisibleChunk(chunk provider.StreamChunk) bool {
	return chunk.Role != "" || chunk.Content != "" || chunk.FinishReason != ""
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
