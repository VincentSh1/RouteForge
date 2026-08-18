package gateway

import (
	"context"
	"errors"
	"testing"

	"github.com/VincentSh1/RouteForge/internal/openai"
)

type recordingProvider struct {
	request openai.ChatCompletionRequest
	err     error
}

func (p *recordingProvider) Name() string { return "recording" }

func (p *recordingProvider) Complete(_ context.Context, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	p.request = req
	return openai.ChatCompletionResponse{Model: req.Model}, p.err
}

func TestServiceCompleteDelegatesToProvider(t *testing.T) {
	p := &recordingProvider{}
	service := New(p)
	req := openai.ChatCompletionRequest{
		Model: "passed-through-model",
		Messages: []openai.Message{
			{Role: "system", Content: "Be concise."},
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi"},
		},
	}

	response, err := service.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if p.request.Model != req.Model || response.Model != req.Model {
		t.Fatalf("model was not passed through: request=%q response=%q", p.request.Model, response.Model)
	}
}

func TestServiceCompleteValidation(t *testing.T) {
	tests := []struct {
		name string
		req  openai.ChatCompletionRequest
		want error
	}{
		{name: "missing model", req: openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "user"}}}, want: ErrModelRequired},
		{name: "missing messages", req: openai.ChatCompletionRequest{Model: "model"}, want: ErrMessagesRequired},
		{name: "streaming", req: openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user"}}, Stream: true}, want: ErrStreamingUnsupported},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(&recordingProvider{}).Complete(context.Background(), test.req)
			if !errors.Is(err, test.want) {
				t.Fatalf("Complete() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestServiceCompleteRejectsUnsupportedRole(t *testing.T) {
	req := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "tool"}}}
	_, err := New(&recordingProvider{}).Complete(context.Background(), req)
	var roleErr *UnsupportedRoleError
	if !errors.As(err, &roleErr) {
		t.Fatalf("Complete() error = %v, want UnsupportedRoleError", err)
	}
}

func TestServiceCompletePropagatesProviderError(t *testing.T) {
	want := errors.New("provider failed")
	p := &recordingProvider{err: want}
	req := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user"}}}
	_, err := New(p).Complete(context.Background(), req)
	if !errors.Is(err, want) {
		t.Fatalf("Complete() error = %v, want %v", err, want)
	}
}
