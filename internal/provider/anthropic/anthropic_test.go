package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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

	p := New(server.Client(), "test-key", server.URL, time.Second)
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
	if response.Usage == nil || response.Usage.InputTokens == nil || *response.Usage.InputTokens != 4 ||
		response.Usage.OutputTokens == nil || *response.Usage.OutputTokens != 2 ||
		response.Usage.TotalTokens == nil || *response.Usage.TotalTokens != 6 {
		t.Fatalf("usage = %+v", response.Usage)
	}
}

func TestCompletePreservesMissingUsageAndRejectsMalformedUsage(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"id":"id","model":"model","content":[]}`))
		}))
		defer server.Close()
		response, err := New(server.Client(), "test-key", server.URL, time.Second).Complete(context.Background(), validRequest())
		if err != nil || response.Usage != nil {
			t.Fatalf("response = %+v, error = %v", response, err)
		}
	})

	for name, upstreamUsage := range map[string]string{
		"partial":  `{"input_tokens":1}`,
		"negative": `{"input_tokens":-1,"output_tokens":2}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"id":"id","model":"model","content":[],"usage":` + upstreamUsage + `}`))
			}))
			defer server.Close()
			_, err := New(server.Client(), "test-key", server.URL, time.Second).Complete(context.Background(), validRequest())
			assertErrorKind(t, err, providerpkg.ErrorInternal)
		})
	}
}

func TestCompleteRejectsMalformedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"content":`))
	}))
	defer server.Close()

	_, err := New(server.Client(), "test-key", server.URL, time.Second).Complete(context.Background(), validRequest())
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

			_, err := New(server.Client(), "test-key", server.URL, time.Second).Complete(context.Background(), validRequest())
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
	_, err := New(server.Client(), "test-key", server.URL, time.Second).Complete(ctx, validRequest())
	assertErrorKind(t, err, providerpkg.ErrorTimeout)
}

func TestCompleteRequiresNonSystemMessage(t *testing.T) {
	p := New(&http.Client{}, "test-key", DefaultBaseURL, time.Second)
	_, err := p.Complete(context.Background(), common.ChatCompletionRequest{
		Model: "claude-test", Messages: []common.Message{{Role: "system", Content: "Instruction"}},
	})
	assertErrorKind(t, err, providerpkg.ErrorInvalidRequest)
}

func TestStreamTranslatesAnthropicEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body request
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if r.URL.Path != "/v1/messages" || !body.Stream || body.System != "Instruction" || body.Model != "claude-test" {
			t.Errorf("unexpected streaming request: path=%s body=%+v", r.URL.Path, body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-test\",\"usage\":{\"input_tokens\":4,\"output_tokens\":0}}}\n\n"))
		_, _ = w.Write([]byte("event: ping\ndata: {\"type\":\"ping\"}\n\n"))
		_, _ = w.Write([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"text\",\"text\":\"Hello\"}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\" there\"}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\"}\n\n"))
		_, _ = w.Write([]byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n"))
		_, _ = w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	stream, err := New(server.Client(), "test-key", server.URL, time.Second).Stream(context.Background(), common.ChatCompletionRequest{
		Model: "claude-test", Messages: []common.Message{{Role: "system", Content: "Instruction"}, {Role: "user", Content: "Hello"}},
	})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	defer stream.Close()
	startUsage, err := stream.Next()
	if err != nil || startUsage.Usage == nil || startUsage.Usage.InputTokens == nil || *startUsage.Usage.InputTokens != 4 || startUsage.Content != "" {
		t.Fatalf("start usage = %+v, %v", startUsage, err)
	}
	first, err := stream.Next()
	if err != nil || first.Role != "assistant" || first.Content != "Hello" {
		t.Fatalf("first chunk = %+v, %v", first, err)
	}
	second, err := stream.Next()
	if err != nil || second.Content != " there" {
		t.Fatalf("second chunk = %+v, %v", second, err)
	}
	finish, err := stream.Next()
	if err != nil || finish.FinishReason != "stop" || finish.Usage == nil || finish.Usage.OutputTokens == nil || *finish.Usage.OutputTokens != 2 ||
		finish.Usage.TotalTokens == nil || *finish.Usage.TotalTokens != 6 {
		t.Fatalf("finish chunk = %+v, %v", finish, err)
	}
	if _, err := stream.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("final error = %v, want EOF", err)
	}
}

func TestStreamRejectsMalformedAndOversizedEvents(t *testing.T) {
	for _, test := range []struct {
		name string
		data string
	}{
		{name: "malformed", data: "not-json"},
		{name: "oversized", data: strings.Repeat("x", providerpkg.MaxSSEEventSize+1)},
		{name: "unsupported delta", data: `{"type":"content_block_delta","delta":{"type":"input_json_delta"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("event: content_block_delta\ndata: " + test.data + "\n\n"))
			}))
			defer server.Close()
			stream, err := New(server.Client(), "test-key", server.URL, time.Second).Stream(context.Background(), validRequest())
			if err != nil {
				t.Fatalf("Stream() error = %v", err)
			}
			defer stream.Close()
			if _, err := stream.Next(); err == nil {
				t.Fatal("Next() error = nil")
			}
		})
	}
}

func TestStreamRejectsMalformedUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"id\",\"model\":\"model\",\"usage\":{\"input_tokens\":-1}}}\n\n"))
	}))
	defer server.Close()
	stream, err := New(server.Client(), "test-key", server.URL, time.Second).Stream(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	defer stream.Close()
	_, err = stream.Next()
	assertErrorKind(t, err, providerpkg.ErrorInternal)
}

func TestStreamMapsTransientStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	_, err := New(server.Client(), "test-key", server.URL, time.Second).Stream(context.Background(), validRequest())
	assertErrorKind(t, err, providerpkg.ErrorRateLimited)
}

func TestStreamRejectsUnexpectedContentType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	_, err := New(server.Client(), "test-key", server.URL, time.Second).Stream(context.Background(), validRequest())
	assertErrorKind(t, err, providerpkg.ErrorInternal)
}

func TestStreamTreatsPrematureEOFAsUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
	}))
	defer server.Close()
	stream, err := New(server.Client(), "test-key", server.URL, time.Second).Stream(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	defer stream.Close()
	_, err = stream.Next()
	assertErrorKind(t, err, providerpkg.ErrorUnavailable)
}

func TestStreamRespectsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	stream, err := New(server.Client(), "test-key", server.URL, time.Second).Stream(ctx, validRequest())
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	defer stream.Close()
	_, err = stream.Next()
	assertErrorKind(t, err, providerpkg.ErrorTimeout)
}

func TestStreamIdleTimeoutAfterResponseHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	stream, err := New(server.Client(), "test-key", server.URL, 25*time.Millisecond).Stream(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	defer stream.Close()
	_, err = stream.Next()
	assertErrorKind(t, err, providerpkg.ErrorTimeout)
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
