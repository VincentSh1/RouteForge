package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	common "github.com/VincentSh1/RouteForge/internal/openai"
	providerpkg "github.com/VincentSh1/RouteForge/internal/provider"
)

func TestCompleteTranslatesRequestAndResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Api-Key") != "test-key" {
			t.Error("API key header was not set")
		}
		if r.Header.Get("Anthropic-Version") != apiVersion {
			t.Errorf("Anthropic-Version = %q", r.Header.Get("Anthropic-Version"))
		}
		var body request
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if body.Model != "claude-test" || body.MaxTokens != defaultMaxTokens {
			t.Errorf("unexpected request settings: %+v", body)
		}
		if body.System != "First instruction\n\nSecond instruction" {
			t.Errorf("system = %q", body.System)
		}
		if len(body.Messages) != 2 || body.Messages[0].Role != "user" || body.Messages[1].Role != "assistant" {
			t.Errorf("messages = %+v", body.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_test","type":"message","role":"assistant","model":"claude-test",
			"content":[{"type":"text","text":"Hello"},{"type":"text","text":" there"}],
			"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":2}
		}`))
	}))
	defer server.Close()

	p := New(server.Client(), "test-key", server.URL)
	response, err := p.Complete(context.Background(), common.ChatCompletionRequest{
		Model: "claude-test",
		Messages: []common.Message{
			{Role: "system", Content: "First instruction"},
			{Role: "user", Content: "Hello"},
			{Role: "system", Content: "Second instruction"},
			{Role: "assistant", Content: "Hi"},
		},
	})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if response.ID != "msg_test" || response.Model != "claude-test" || response.Choices[0].Message.Content != "Hello there" {
		t.Fatalf("unexpected response: %+v", response)
	}
	if response.Usage.PromptTokens != 4 || response.Usage.CompletionTokens != 2 || response.Usage.TotalTokens != 6 {
		t.Fatalf("usage = %+v", response.Usage)
	}
}

func TestCompleteRejectsMalformedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"content":`))
	}))
	defer server.Close()

	_, err := New(server.Client(), "test-key", server.URL).Complete(context.Background(), validRequest())
	assertErrorKind(t, err, providerpkg.ErrorInternal)
}

func TestCompleteMapsStatus(t *testing.T) {
	tests := []struct {
		status int
		kind   providerpkg.ErrorKind
	}{
		{status: http.StatusTooManyRequests, kind: providerpkg.ErrorRateLimited},
		{status: http.StatusInternalServerError, kind: providerpkg.ErrorUnavailable},
	}
	for _, test := range tests {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(`{"error":"sensitive upstream detail"}`))
			}))
			defer server.Close()

			_, err := New(server.Client(), "test-key", server.URL).Complete(context.Background(), validRequest())
			assertErrorKind(t, err, test.kind)
			if err != nil && strings.Contains(err.Error(), "sensitive upstream detail") {
				t.Fatal("upstream body leaked through error")
			}
		})
	}
}

func TestCompleteRespectsContextDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err := New(server.Client(), "test-key", server.URL).Complete(ctx, validRequest())
	assertErrorKind(t, err, providerpkg.ErrorTimeout)
}

func TestCompleteRequiresNonSystemMessage(t *testing.T) {
	p := New(&http.Client{}, "test-key", DefaultBaseURL)
	_, err := p.Complete(context.Background(), common.ChatCompletionRequest{
		Model: "claude-test", Messages: []common.Message{{Role: "system", Content: "Instruction"}},
	})
	assertErrorKind(t, err, providerpkg.ErrorInvalidRequest)
}

func validRequest() common.ChatCompletionRequest {
	return common.ChatCompletionRequest{Model: "claude-test", Messages: []common.Message{{Role: "user", Content: "Hello"}}}
}

func assertErrorKind(t *testing.T, err error, want providerpkg.ErrorKind) {
	t.Helper()
	var providerErr *providerpkg.Error
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %v, want provider error", err)
	}
	if providerErr.Kind != want {
		t.Fatalf("error kind = %q, want %q", providerErr.Kind, want)
	}
}
