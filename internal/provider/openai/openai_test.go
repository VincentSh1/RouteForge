package openai

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

	p := New(server.Client(), "test-key", server.URL, time.Second)
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
	if response.Usage == nil || response.Usage.InputTokens == nil || *response.Usage.InputTokens != 3 ||
		response.Usage.OutputTokens == nil || *response.Usage.OutputTokens != 2 ||
		response.Usage.TotalTokens == nil || *response.Usage.TotalTokens != 5 {
		t.Fatalf("usage = %+v", response.Usage)
	}
}

func TestCompletePreservesMissingUsageAndRejectsMalformedUsage(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"id":"id","model":"model","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
		}))
		defer server.Close()
		response, err := New(server.Client(), "test-key", server.URL, time.Second).Complete(context.Background(), validRequest())
		if err != nil || response.Usage != nil {
			t.Fatalf("response = %+v, error = %v", response, err)
		}
	})

	for name, upstreamUsage := range map[string]string{
		"partial":      `{"prompt_tokens":1}`,
		"inconsistent": `{"prompt_tokens":1,"completion_tokens":2,"total_tokens":9}`,
		"negative":     `{"prompt_tokens":-1,"completion_tokens":2,"total_tokens":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"id":"id","model":"model","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":` + upstreamUsage + `}`))
			}))
			defer server.Close()
			_, err := New(server.Client(), "test-key", server.URL, time.Second).Complete(context.Background(), validRequest())
			assertErrorKind(t, err, providerpkg.ErrorInternal)
		})
	}
}

func TestCompleteRejectsMalformedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":`))
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
		{status: http.StatusServiceUnavailable, kind: providerpkg.ErrorUnavailable},
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

func TestStreamTranslatesIncrementalSSE(t *testing.T) {
	continueStream := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body request
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if r.URL.Path != "/v1/chat/completions" || !body.Stream || body.Model != "gpt-test" || body.StreamOptions == nil || !body.StreamOptions.IncludeUsage {
			t.Errorf("unexpected streaming request: path=%s body=%+v", r.URL.Path, body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"one\",\"created\":1,\"model\":\"gpt-test\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"Hello\"},\"finish_reason\":null}]}\n\n"))
		w.(http.Flusher).Flush()
		<-continueStream
		_, _ = w.Write([]byte("data: {\"id\":\"one\",\"created\":1,\"model\":\"gpt-test\",\"choices\":[{\"delta\":{\"content\":\" there\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"one\",\"created\":1,\"model\":\"gpt-test\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	stream, err := New(server.Client(), "test-key", server.URL, time.Second).Stream(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	defer stream.Close()
	first, err := stream.Next()
	if err != nil || first.Role != "assistant" || first.Content != "Hello" {
		t.Fatalf("first chunk = %+v, %v", first, err)
	}
	close(continueStream)
	second, err := stream.Next()
	if err != nil || second.Content != " there" {
		t.Fatalf("second chunk = %+v, %v", second, err)
	}
	usageChunk, err := stream.Next()
	if err != nil || usageChunk.Content != "" || usageChunk.Usage == nil || usageChunk.Usage.TotalTokens == nil || *usageChunk.Usage.TotalTokens != 5 {
		t.Fatalf("usage chunk = %+v, %v", usageChunk, err)
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
		{name: "malformed usage", data: `{"choices":[],"usage":{"prompt_tokens":1}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: " + test.data + "\n\n"))
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

func TestStreamMapsTransientStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	_, err := New(server.Client(), "test-key", server.URL, time.Second).Stream(context.Background(), validRequest())
	assertErrorKind(t, err, providerpkg.ErrorUnavailable)
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

func TestStreamIdleTimeoutBeforeResponse(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-release
	}))
	defer server.Close()
	defer close(release)

	_, err := New(server.Client(), "test-key", server.URL, 25*time.Millisecond).Stream(context.Background(), validRequest())
	assertErrorKind(t, err, providerpkg.ErrorTimeout)
}

func TestStreamActivityCanOutliveClientTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		for _, content := range []string{"one", "two", "three", "four"} {
			time.Sleep(30 * time.Millisecond)
			_, _ = w.Write([]byte("data: {\"id\":\"id\",\"created\":1,\"model\":\"gpt-test\",\"choices\":[{\"delta\":{\"content\":\"" + content + "\"},\"finish_reason\":null}]}\n\n"))
			w.(http.Flusher).Flush()
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := server.Client()
	client.Timeout = 50 * time.Millisecond
	stream, err := New(client, "test-key", server.URL, 100*time.Millisecond).Stream(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	defer stream.Close()
	started := time.Now()
	chunks := 0
	for {
		_, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next() error = %v", err)
		}
		chunks++
	}
	if chunks != 4 || time.Since(started) <= client.Timeout {
		t.Fatalf("chunks = %d, elapsed = %v", chunks, time.Since(started))
	}
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
