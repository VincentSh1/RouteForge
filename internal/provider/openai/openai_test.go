package openai

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
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("authorization header was not set")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		var body request
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if body.Model != "gpt-test" || len(body.Messages) != 2 || body.Messages[0].Role != "system" || body.Messages[1].Content != "Hello" {
			t.Errorf("unexpected request: %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-test","object":"chat.completion","created":123,"model":"gpt-test",
			"choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}
		}`))
	}))
	defer server.Close()

	p := New(server.Client(), "test-key", server.URL)
	response, err := p.Complete(context.Background(), common.ChatCompletionRequest{
		Model: "gpt-test",
		Messages: []common.Message{
			{Role: "system", Content: "Be concise"},
			{Role: "user", Content: "Hello"},
		},
	})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if response.ID != "chatcmpl-test" || response.Model != "gpt-test" || response.Choices[0].Message.Content != "Hi" {
		t.Fatalf("unexpected response: %+v", response)
	}
	if response.Usage.TotalTokens != 5 {
		t.Fatalf("usage = %+v", response.Usage)
	}
}

func TestCompleteRejectsMalformedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":`))
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
		{status: http.StatusServiceUnavailable, kind: providerpkg.ErrorUnavailable},
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

func validRequest() common.ChatCompletionRequest {
	return common.ChatCompletionRequest{Model: "gpt-test", Messages: []common.Message{{Role: "user", Content: "Hello"}}}
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
