package mock

import (
	"context"
	"io"
	"time"

	"github.com/VincentSh1/RouteForge/internal/openai"
	"github.com/VincentSh1/RouteForge/internal/provider"
)

// Provider is a deterministic local provider used by Phase 1 and its tests.
type Provider struct {
	ResponseText   string
	Err            error
	StreamChunks   []string
	StreamErr      error
	StreamErrAfter int
}

func (p *Provider) Stream(ctx context.Context, req openai.ChatCompletionRequest) (provider.Stream, error) {
	if p.StreamErr != nil && p.StreamErrAfter == 0 {
		return nil, p.StreamErr
	}
	chunks := p.StreamChunks
	if len(chunks) == 0 {
		chunks = []string{"Hello", " from", " RouteForge."}
	}
	return &stream{ctx: ctx, model: req.Model, chunks: chunks, err: p.StreamErr, errAfter: p.StreamErrAfter}, nil
}

type stream struct {
	ctx      context.Context
	model    string
	chunks   []string
	err      error
	errAfter int
	index    int
	finished bool
}

func (s *stream) Next() (provider.StreamChunk, error) {
	if err := s.ctx.Err(); err != nil {
		return provider.StreamChunk{}, err
	}
	if s.err != nil && s.index == s.errAfter {
		return provider.StreamChunk{}, s.err
	}
	if s.index < len(s.chunks) {
		chunk := provider.StreamChunk{
			ID:      "chatcmpl-routeforge-mock",
			Created: time.Now().Unix(),
			Model:   s.model,
			Content: s.chunks[s.index],
		}
		if s.index == 0 {
			chunk.Role = "assistant"
		}
		s.index++
		return chunk, nil
	}
	if !s.finished {
		s.finished = true
		return provider.StreamChunk{
			ID: "chatcmpl-routeforge-mock", Created: time.Now().Unix(), Model: s.model,
			FinishReason: "stop", Usage: openai.NewUsage(3, 4, 7),
		}, nil
	}
	return provider.StreamChunk{}, io.EOF
}

func (s *stream) Close() error { return nil }

const Name = "mock"

func (p *Provider) Name() string { return Name }

func (p *Provider) Complete(_ context.Context, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if p.Err != nil {
		return openai.ChatCompletionResponse{}, p.Err
	}

	text := p.ResponseText
	if text == "" {
		text = "Hello from RouteForge's mock provider."
	}

	return openai.ChatCompletionResponse{
		ID:      "chatcmpl-routeforge-mock",
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []openai.Choice{{
			Index:        0,
			Message:      openai.Message{Role: "assistant", Content: text},
			FinishReason: "stop",
		}},
		Usage: openai.NewUsage(3, 4, 7),
	}, nil
}
