package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/VincentSh1/RouteForge/internal/gateway"
	"github.com/VincentSh1/RouteForge/internal/openai"
	providerpkg "github.com/VincentSh1/RouteForge/internal/provider"
	"github.com/VincentSh1/RouteForge/internal/provider/mock"
)

func testHandler(p *mock.Provider) http.Handler {
	return NewHandler(gateway.New(p)).Routes()
}

func TestHealth(t *testing.T) {
	recorder := httptest.NewRecorder()
	testHandler(&mock.Provider{}).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
}

func TestChatCompletions(t *testing.T) {
	body := []byte(`{"model":"client-model","messages":[{"role":"user","content":"Hello"}],"unknown":"ignored"}`)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	testHandler(&mock.Provider{ResponseText: "Mock answer"}).ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var response openai.ChatCompletionResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Model != "client-model" || response.Choices[0].Message.Content != "Mock answer" {
		t.Fatalf("unexpected response: %+v", response)
	}
}

func TestChatCompletionsValidationErrors(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		param string
		code  string
	}{
		{name: "model", body: `{"messages":[{"role":"user","content":"Hello"}]}`, param: "model", code: "model_required"},
		{name: "messages", body: `{"model":"model"}`, param: "messages", code: "messages_required"},
		{name: "stream", body: `{"model":"model","messages":[{"role":"user"}],"stream":true}`, param: "stream", code: "unsupported_parameter"},
		{name: "role", body: `{"model":"model","messages":[{"role":"tool"}]}`, param: "messages", code: "unsupported_value"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(test.body))
			testHandler(&mock.Provider{}).ServeHTTP(recorder, req)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
			}
			var response openai.ErrorResponse
			if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if response.Error.Param == nil || *response.Error.Param != test.param || response.Error.Code != test.code {
				t.Fatalf("unexpected error response: %+v", response)
			}
		})
	}
}

func TestChatCompletionsRejectsMalformedOrMultipleJSON(t *testing.T) {
	for _, body := range []string{`{"model":`, `{} {}`} {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
		testHandler(&mock.Provider{}).ServeHTTP(recorder, req)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %q: status = %d, want %d", body, recorder.Code, http.StatusBadRequest)
		}
	}
}

func TestChatCompletionsMapsProviderError(t *testing.T) {
	providerErr := providerpkg.NewError(providerpkg.ErrorRateLimited, "mock", errors.New("quota"))
	body := `{"model":"model","messages":[{"role":"user"}]}`
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
	testHandler(&mock.Provider{Err: providerErr}).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
}

func TestMethodsAreEnforced(t *testing.T) {
	tests := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/health"},
		{http.MethodGet, "/v1/chat/completions"},
	}
	for _, test := range tests {
		recorder := httptest.NewRecorder()
		testHandler(&mock.Provider{}).ServeHTTP(recorder, httptest.NewRequest(test.method, test.path, nil))
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s: status = %d, want %d", test.method, test.path, recorder.Code, http.StatusMethodNotAllowed)
		}
	}
}
