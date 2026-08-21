package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/VincentSh1/RouteForge/internal/gateway"
	"github.com/VincentSh1/RouteForge/internal/model"
	"github.com/VincentSh1/RouteForge/internal/openai"
	providerpkg "github.com/VincentSh1/RouteForge/internal/provider"
	"github.com/VincentSh1/RouteForge/internal/provider/mock"
)

func testHandler(p *mock.Provider) http.Handler {
	return NewHandler(gateway.New(p, model.New(nil))).Routes()
}

func chatRequest(method, body string) *http.Request {
	req := httptest.NewRequest(method, "/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	return req
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
	req := chatRequest(http.MethodPost, string(body))
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
			req := chatRequest(http.MethodPost, test.body)
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
		req := chatRequest(http.MethodPost, body)
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
	req := chatRequest(http.MethodPost, body)
	testHandler(&mock.Provider{Err: providerErr}).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
	if bytes.Contains(recorder.Body.Bytes(), []byte("quota")) {
		t.Fatal("response exposed raw provider error")
	}
}

func TestChatCompletionsSanitizesFinalFallbackError(t *testing.T) {
	first := &mock.Provider{Err: providerpkg.NewError(providerpkg.ErrorUnavailable, "first", errors.New("first secret"))}
	second := &mock.Provider{Err: providerpkg.NewError(providerpkg.ErrorRateLimited, "second", errors.New("second secret"))}
	resolver := model.New(map[string]map[string]string{model.General: {"mock": "mock-model"}})
	handler := NewHandler(gateway.NewAuto(resolver, first, second)).Routes()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, chatRequest(http.MethodPost, `{"model":"routeforge/general","messages":[{"role":"user"}]}`))
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
	if bytes.Contains(recorder.Body.Bytes(), []byte("secret")) {
		t.Fatal("response exposed a raw fallback error")
	}
}

func TestChatCompletionsMapsModelResolutionErrors(t *testing.T) {
	tests := []struct {
		name       string
		service    *gateway.Service
		model      string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "unknown alias",
			service:    gateway.New(&mock.Provider{}, model.New(nil)),
			model:      "routeforge/unknown",
			wantStatus: http.StatusBadRequest,
			wantCode:   "unknown_model",
		},
		{
			name:       "missing mapping",
			service:    gateway.New(&mock.Provider{}, model.New(map[string]map[string]string{model.General: {}})),
			model:      model.General,
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   "model_unavailable",
		},
		{
			name:       "native model in auto",
			service:    gateway.NewAuto(model.New(nil), &mock.Provider{}),
			model:      "provider-native-model",
			wantStatus: http.StatusBadRequest,
			wantCode:   "provider_required",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHandler(test.service).Routes()
			body := `{"model":"` + test.model + `","messages":[{"role":"user"}]}`
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, chatRequest(http.MethodPost, body))
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, test.wantStatus)
			}
			var response openai.ErrorResponse
			if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if response.Error.Code != test.wantCode {
				t.Fatalf("error code = %q, want %q", response.Error.Code, test.wantCode)
			}
		})
	}
}

func TestChatCompletionsRejectsUnsupportedContentType(t *testing.T) {
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "text/plain")
	testHandler(&mock.Provider{}).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnsupportedMediaType)
	}
}

func TestChatCompletionsRejectsOversizedBody(t *testing.T) {
	recorder := httptest.NewRecorder()
	body := `{"model":"` + string(bytes.Repeat([]byte("x"), maxRequestBodyBytes)) + `","messages":[{"role":"user"}]}`
	req := chatRequest(http.MethodPost, body)
	testHandler(&mock.Provider{}).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if !bytes.Contains(recorder.Body.Bytes(), []byte("request body is too large")) {
		t.Fatalf("response did not identify body-size violation: %s", recorder.Body.String())
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
		req := httptest.NewRequest(test.method, test.path, nil)
		testHandler(&mock.Provider{}).ServeHTTP(recorder, req)
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s: status = %d, want %d", test.method, test.path, recorder.Code, http.StatusMethodNotAllowed)
		}
	}
}
