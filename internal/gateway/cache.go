package gateway

import (
	"context"
	"time"

	"github.com/VincentSh1/RouteForge/internal/cache"
	"github.com/VincentSh1/RouteForge/internal/openai"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// SetCache is a startup-only dependency injection point, like SetPersistence.
func (s *Service) SetCache(store cache.Store, ttl time.Duration) {
	if store == nil {
		store = cache.Noop{}
	}
	if ttl < time.Millisecond {
		ttl = cache.DefaultTTL
	}
	s.responseCache, s.cacheTTL = store, ttl
}

func (s *Service) lookupCompletion(ctx context.Context, key string) (openai.ChatCompletionResponse, bool) {
	if key == "" {
		return openai.ChatCompletionResponse{}, false
	}
	opCtx, cancel := context.WithTimeout(ctx, cache.OperationTimeout)
	defer cancel()
	data, err := s.responseCache.Get(opCtx, key)
	result := "miss"
	var response openai.ChatCompletionResponse
	if err != nil {
		result = "error"
	} else if len(data) > 0 {
		response, err = cache.Decode(data)
		if err != nil {
			result = "error"
		} else {
			result = "hit"
		}
	}
	s.metrics.RecordCacheLookup(ctx, result)
	trace.SpanFromContext(ctx).AddEvent("routeforge.cache", trace.WithAttributes(attribute.String("cache.result", result)))
	return response, result == "hit"
}

func (s *Service) storeCompletion(ctx context.Context, key string, response openai.ChatCompletionResponse) {
	data, err := cache.Encode(response)
	if err != nil {
		return
	}
	opCtx, cancel := context.WithTimeout(ctx, cache.OperationTimeout)
	defer cancel()
	result := "success"
	if s.responseCache.Set(opCtx, key, data, s.cacheTTL) != nil {
		result = "error"
	}
	s.metrics.RecordCacheWrite(ctx, result)
}
