package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/VincentSh1/RouteForge/internal/gateway"
	"github.com/VincentSh1/RouteForge/internal/model"
	providerpkg "github.com/VincentSh1/RouteForge/internal/provider"
	"github.com/VincentSh1/RouteForge/internal/provider/mock"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestRequestTracingHierarchyAndPrivacy(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	tracer := provider.Tracer("routeforge-test")
	service := gateway.New(&mock.Provider{ResponseText: "private response content"}, model.New(map[string]map[string]string{
		model.General: {mock.Name: "mock-model"},
	}))
	service.SetTracer(tracer)
	handler := TraceRequests(NewHandler(service).Routes(), tracer, propagation.TraceContext{}, gateway.RoutingPolicyLatency)

	request := chatRequest(http.MethodPost, `{"model":"routeforge/general","messages":[{"role":"user","content":"private prompt content"}]}`)
	request.Header.Set("Authorization", "Bearer private-credential-value")
	request.Header.Set("Traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	recorderHTTP := httptest.NewRecorder()
	handler.ServeHTTP(recorderHTTP, request)
	if recorderHTTP.Code != http.StatusOK {
		t.Fatalf("status = %d", recorderHTTP.Code)
	}

	spans := recorder.Ended()
	requestSpans := httpSpansNamed(spans, "routeforge.request")
	if len(requestSpans) != 1 {
		t.Fatalf("request spans = %d", len(requestSpans))
	}
	requestSpan := requestSpans[0]
	assertHTTPSpanAttribute(t, requestSpan, "http.request.method", http.MethodPost)
	assertHTTPSpanAttribute(t, requestSpan, "http.route", "/v1/chat/completions")
	assertHTTPSpanAttribute(t, requestSpan, "http.response.status_code", int64(http.StatusOK))
	assertHTTPSpanAttribute(t, requestSpan, "routeforge.routing.policy", gateway.RoutingPolicyLatency)
	assertHTTPSpanAttribute(t, requestSpan, "routeforge.request.streaming", false)
	assertHTTPSpanAttribute(t, requestSpan, "routeforge.request.model_alias", model.General)
	assertHTTPSpanAttribute(t, requestSpan, "routeforge.response.outcome", "success")
	if got := requestSpan.Parent().SpanID().String(); got != "00f067aa0ba902b7" {
		t.Fatalf("incoming parent span ID = %s", got)
	}

	for _, childName := range []string{"routeforge.routing", "routeforge.provider.attempt"} {
		children := httpSpansNamed(spans, childName)
		if len(children) != 1 || children[0].Parent().SpanID() != requestSpan.SpanContext().SpanID() {
			t.Fatalf("%s hierarchy is incorrect", childName)
		}
	}
	for _, forbidden := range []string{
		"private prompt content", "private response content", "private-credential-value", "Authorization", "/Users/private",
	} {
		if httpSpansContain(spans, forbidden) {
			t.Fatalf("span data contains forbidden value %q", forbidden)
		}
	}
}

func TestMalformedRequestProducesOnlySanitizedRequestSpan(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	tracer := provider.Tracer("routeforge-test")
	handler := TraceRequests(testHandler(&mock.Provider{}), tracer, propagation.TraceContext{}, gateway.RoutingPolicyDeterministic)

	recorderHTTP := httptest.NewRecorder()
	handler.ServeHTTP(recorderHTTP, chatRequest(http.MethodPost, `{"model":`))
	if recorderHTTP.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", recorderHTTP.Code)
	}
	spans := recorder.Ended()
	if len(httpSpansNamed(spans, "routeforge.request")) != 1 || len(httpSpansNamed(spans, "routeforge.provider.attempt")) != 0 {
		t.Fatalf("spans = %v", spanNames(spans))
	}
	assertHTTPSpanAttribute(t, httpSpansNamed(spans, "routeforge.request")[0], "routeforge.response.outcome", "client_error")
	if httpSpansContain(spans, `{"model":`) {
		t.Fatal("malformed request body was attached to a span")
	}
}

func TestStreamingRequestTracingUsesOneAttemptSpan(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	tracer := provider.Tracer("routeforge-test")
	service := gateway.New(&mock.Provider{StreamChunks: []string{"one", "two", "three"}}, model.New(nil))
	service.SetTracer(tracer)
	handler := TraceRequests(NewHandler(service).Routes(), tracer, propagation.TraceContext{}, gateway.RoutingPolicyDeterministic)

	recorderHTTP := httptest.NewRecorder()
	handler.ServeHTTP(recorderHTTP, chatRequest(http.MethodPost, `{"model":"mock-model","messages":[{"role":"user"}],"stream":true}`))
	if recorderHTTP.Code != http.StatusOK {
		t.Fatalf("status = %d", recorderHTTP.Code)
	}
	spans := recorder.Ended()
	requests := httpSpansNamed(spans, "routeforge.request")
	attempts := httpSpansNamed(spans, "routeforge.provider.attempt")
	if len(requests) != 1 || len(attempts) != 1 || len(spans) != 3 {
		t.Fatalf("spans = %v", spanNames(spans))
	}
	assertHTTPSpanAttribute(t, requests[0], "routeforge.request.streaming", true)
	firstContentEvents := 0
	for _, event := range attempts[0].Events() {
		if event.Name == "routeforge.first_content" {
			firstContentEvents++
		}
	}
	if firstContentEvents != 1 {
		t.Fatalf("first-content events = %d", firstContentEvents)
	}
}

func TestCommittedStreamingFailureEndsRequestSpanHonestly(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	tracer := provider.Tracer("routeforge-test")
	service := gateway.New(&mock.Provider{
		StreamChunks:   []string{"partial"},
		StreamErr:      providerpkg.NewError(providerpkg.ErrorUnavailable, mock.Name, errors.New("private detail")),
		StreamErrAfter: 1,
	}, model.New(nil))
	service.SetTracer(tracer)
	handler := TraceRequests(NewHandler(service).Routes(), tracer, propagation.TraceContext{}, gateway.RoutingPolicyDeterministic)

	recorderHTTP := httptest.NewRecorder()
	handler.ServeHTTP(recorderHTTP, chatRequest(http.MethodPost, `{"model":"mock-model","messages":[{"role":"user"}],"stream":true}`))
	requests := httpSpansNamed(recorder.Ended(), "routeforge.request")
	if len(requests) != 1 {
		t.Fatalf("request spans = %d", len(requests))
	}
	assertHTTPSpanAttribute(t, requests[0], "http.response.status_code", int64(http.StatusOK))
	assertHTTPSpanAttribute(t, requests[0], "routeforge.response.outcome", "failure")
	if requests[0].Status().Code == 0 {
		t.Fatal("post-commit stream failure left request span status unset")
	}
	if httpSpansContain(recorder.Ended(), "private detail") {
		t.Fatal("provider failure detail was attached to a span")
	}
}

func TestDisabledTracingPreservesStreamingHTTPBehavior(t *testing.T) {
	tracer := noop.NewTracerProvider().Tracer("routeforge-test")
	handler := TraceRequests(testHandler(&mock.Provider{}), tracer, propagation.TraceContext{}, gateway.RoutingPolicyDeterministic)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, chatRequest(http.MethodPost, `{"model":"mock-model","messages":[{"role":"user"}],"stream":true}`))
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "text/event-stream" || !recorder.Flushed {
		t.Fatalf("status=%d content-type=%q flushed=%v", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Flushed)
	}
	if !strings.HasSuffix(strings.TrimSpace(recorder.Body.String()), "data: [DONE]") {
		t.Fatalf("stream body = %q", recorder.Body.String())
	}
}

func httpSpansNamed(spans []sdktrace.ReadOnlySpan, name string) []sdktrace.ReadOnlySpan {
	var matches []sdktrace.ReadOnlySpan
	for _, span := range spans {
		if span.Name() == name {
			matches = append(matches, span)
		}
	}
	return matches
}

func assertHTTPSpanAttribute(t *testing.T, span sdktrace.ReadOnlySpan, key string, want any) {
	t.Helper()
	for _, item := range span.Attributes() {
		if string(item.Key) == key {
			if got := item.Value.AsInterface(); got != want {
				t.Fatalf("attribute %s = %v, want %v", key, got, want)
			}
			return
		}
	}
	t.Fatalf("attribute %s is absent", key)
}

func httpSpansContain(spans []sdktrace.ReadOnlySpan, forbidden string) bool {
	for _, span := range spans {
		if strings.Contains(span.Name(), forbidden) {
			return true
		}
		for _, item := range span.Attributes() {
			if strings.Contains(item.Value.Emit(), forbidden) || strings.Contains(string(item.Key), forbidden) {
				return true
			}
		}
		for _, event := range span.Events() {
			if strings.Contains(event.Name, forbidden) {
				return true
			}
			for _, item := range event.Attributes {
				if strings.Contains(item.Value.Emit(), forbidden) || strings.Contains(string(item.Key), forbidden) {
					return true
				}
			}
		}
	}
	return false
}

func spanNames(spans []sdktrace.ReadOnlySpan) []string {
	names := make([]string, len(spans))
	for i, span := range spans {
		names[i] = span.Name()
	}
	return names
}
