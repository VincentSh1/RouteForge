package mock

import (
	"context"
	"time"

	"github.com/VincentSh1/RouteForge/internal/openai"
)

// Provider is a deterministic local provider used by Phase 1 and its tests.
type Provider struct {
	ResponseText string
	Err          error
}

func (p *Provider) Name() string { return "mock" }

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
		Usage: openai.Usage{},
	}, nil
}
