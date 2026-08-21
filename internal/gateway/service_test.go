package gateway

import (
	"context"
	"errors"
	"testing"

	"github.com/VincentSh1/RouteForge/internal/model"
	"github.com/VincentSh1/RouteForge/internal/openai"
	"github.com/VincentSh1/RouteForge/internal/provider"
)

type recordingProvider struct {
	name    string
	request openai.ChatCompletionRequest
	err     error
	calls   int
}

func (p *recordingProvider) Name() string {
	if p.name == "" {
		return "recording"
	}
	return p.name
}

func (p *recordingProvider) Complete(_ context.Context, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	p.calls++
	p.request = req
	return openai.ChatCompletionResponse{ID: p.Name(), Model: req.Model}, p.err
}

func TestServiceCompleteDelegatesToProvider(t *testing.T) {
	p := &recordingProvider{}
	service := New(p, testResolver())
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
			_, err := New(&recordingProvider{}, testResolver()).Complete(context.Background(), test.req)
			if !errors.Is(err, test.want) {
				t.Fatalf("Complete() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestServiceCompleteRejectsUnsupportedRole(t *testing.T) {
	req := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "tool"}}}
	_, err := New(&recordingProvider{}, testResolver()).Complete(context.Background(), req)
	var roleErr *UnsupportedRoleError
	if !errors.As(err, &roleErr) {
		t.Fatalf("Complete() error = %v, want UnsupportedRoleError", err)
	}
}

func TestServiceCompletePropagatesProviderError(t *testing.T) {
	want := errors.New("provider failed")
	p := &recordingProvider{err: want}
	req := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user"}}}
	_, err := New(p, testResolver()).Complete(context.Background(), req)
	if !errors.Is(err, want) {
		t.Fatalf("Complete() error = %v, want %v", err, want)
	}
}

func TestAutoUsesFirstConfiguredProvider(t *testing.T) {
	first := &recordingProvider{name: "first"}
	second := &recordingProvider{name: "second"}
	response, err := NewAuto(testResolver(), first, second).Complete(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if response.ID != "first" || first.calls != 1 || second.calls != 0 {
		t.Fatalf("calls = first:%d second:%d; response=%+v", first.calls, second.calls, response)
	}
}

func TestAutoFallsBackAfterTransientFailure(t *testing.T) {
	for _, kind := range []provider.ErrorKind{provider.ErrorUnavailable, provider.ErrorTimeout, provider.ErrorRateLimited} {
		t.Run(string(kind), func(t *testing.T) {
			first := &recordingProvider{name: "first", err: provider.NewError(kind, "first", errors.New("transient"))}
			second := &recordingProvider{name: "second"}
			response, err := NewAuto(testResolver(), first, second).Complete(context.Background(), validRequest())
			if err != nil {
				t.Fatalf("Complete() error = %v", err)
			}
			if response.ID != "second" || first.calls != 1 || second.calls != 1 {
				t.Fatalf("calls = first:%d second:%d; response=%+v", first.calls, second.calls, response)
			}
		})
	}
}

func TestAutoDoesNotFallBackAfterInvalidRequest(t *testing.T) {
	first := &recordingProvider{err: provider.NewError(provider.ErrorInvalidRequest, "first", errors.New("invalid"))}
	second := &recordingProvider{}
	_, err := NewAuto(testResolver(), first, second).Complete(context.Background(), validRequest())
	var providerErr *provider.Error
	if !errors.As(err, &providerErr) || providerErr.Kind != provider.ErrorInvalidRequest {
		t.Fatalf("Complete() error = %v", err)
	}
	if first.calls != 1 || second.calls != 0 {
		t.Fatalf("calls = first:%d second:%d", first.calls, second.calls)
	}
}

func TestAutoAttemptsEachProviderOnce(t *testing.T) {
	first := &recordingProvider{err: provider.NewError(provider.ErrorUnavailable, "first", errors.New("unavailable"))}
	secondErr := provider.NewError(provider.ErrorRateLimited, "second", errors.New("limited"))
	second := &recordingProvider{err: secondErr}
	_, err := NewAuto(testResolver(), first, second).Complete(context.Background(), validRequest())
	if !errors.Is(err, secondErr) {
		t.Fatalf("Complete() error = %v, want final provider error", err)
	}
	if first.calls != 1 || second.calls != 1 {
		t.Fatalf("calls = first:%d second:%d", first.calls, second.calls)
	}
}

func TestAutoStopsWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	first := &recordingProvider{err: provider.NewError(provider.ErrorTimeout, "first", context.Canceled)}
	second := &recordingProvider{}
	_, _ = NewAuto(testResolver(), first, second).Complete(ctx, validRequest())
	if second.calls != 0 {
		t.Fatalf("second provider calls = %d, want 0", second.calls)
	}
}

func TestAutoResolvesOriginalAliasForEachProvider(t *testing.T) {
	first := &recordingProvider{name: "first", err: provider.NewError(provider.ErrorUnavailable, "first", errors.New("unavailable"))}
	second := &recordingProvider{name: "second"}
	req := validRequest()

	response, err := NewAuto(testResolver(), first, second).Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if first.request.Model != "first-model" {
		t.Fatalf("first model = %q, want first-model", first.request.Model)
	}
	if second.request.Model != "second-model" {
		t.Fatalf("second model = %q, want second-model", second.request.Model)
	}
	if req.Model != model.General {
		t.Fatalf("original model mutated to %q", req.Model)
	}
	if response.Model != model.General {
		t.Fatalf("response model = %q, want %q", response.Model, model.General)
	}
}

func TestExplicitProviderNativeModelPassesThrough(t *testing.T) {
	p := &recordingProvider{name: "openai"}
	req := openai.ChatCompletionRequest{Model: "provider-native-model", Messages: []openai.Message{{Role: "user"}}}
	_, err := New(p, testResolver()).Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if p.request.Model != req.Model {
		t.Fatalf("provider model = %q, want %q", p.request.Model, req.Model)
	}
}

func TestAutoRejectsProviderNativeModel(t *testing.T) {
	p := &recordingProvider{name: "first"}
	req := openai.ChatCompletionRequest{Model: "provider-native-model", Messages: []openai.Message{{Role: "user"}}}
	_, err := NewAuto(testResolver(), p).Complete(context.Background(), req)
	if !errors.Is(err, ErrNativeModelInAuto) {
		t.Fatalf("Complete() error = %v, want %v", err, ErrNativeModelInAuto)
	}
	if p.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", p.calls)
	}
}

func TestLogicalModelResolutionErrors(t *testing.T) {
	t.Run("unknown alias", func(t *testing.T) {
		req := openai.ChatCompletionRequest{Model: "routeforge/unknown", Messages: []openai.Message{{Role: "user"}}}
		_, err := New(&recordingProvider{name: "openai"}, testResolver()).Complete(context.Background(), req)
		var resolutionErr *model.ResolutionError
		if !errors.As(err, &resolutionErr) || resolutionErr.Kind != model.ErrorUnknownAlias {
			t.Fatalf("Complete() error = %v", err)
		}
	})

	t.Run("missing explicit mapping", func(t *testing.T) {
		_, err := New(&recordingProvider{name: "unmapped"}, testResolver()).Complete(context.Background(), validRequest())
		var resolutionErr *model.ResolutionError
		if !errors.As(err, &resolutionErr) || resolutionErr.Kind != model.ErrorMissingMapping {
			t.Fatalf("Complete() error = %v", err)
		}
	})

	t.Run("no usable auto mapping", func(t *testing.T) {
		_, err := NewAuto(testResolver(), &recordingProvider{name: "unmapped"}).Complete(context.Background(), validRequest())
		if !errors.Is(err, ErrNoUsableModelMapping) {
			t.Fatalf("Complete() error = %v, want %v", err, ErrNoUsableModelMapping)
		}
	})
}

func TestAutoSkipsProviderWithoutMapping(t *testing.T) {
	first := &recordingProvider{name: "unmapped"}
	second := &recordingProvider{name: "second"}
	response, err := NewAuto(testResolver(), first, second).Complete(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if first.calls != 0 || second.calls != 1 || response.ID != "second" {
		t.Fatalf("calls = first:%d second:%d; response=%+v", first.calls, second.calls, response)
	}
}

func validRequest() openai.ChatCompletionRequest {
	return openai.ChatCompletionRequest{Model: model.General, Messages: []openai.Message{{Role: "user", Content: "Hello"}}}
}

func testResolver() *model.Resolver {
	return model.New(map[string]map[string]string{
		model.General: {
			"recording": "recording-model",
			"first":     "first-model",
			"second":    "second-model",
		},
	})
}
