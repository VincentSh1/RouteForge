package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/VincentSh1/RouteForge/internal/model"
	"github.com/VincentSh1/RouteForge/internal/provider"
	"github.com/VincentSh1/RouteForge/internal/provider/anthropic"
	openaiadapter "github.com/VincentSh1/RouteForge/internal/provider/openai"
)

type bodyTransport struct{ body func() io.ReadCloser }

func (r bodyTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: r.body()}, nil
}

type failingResponseBody struct{ err error }

func (r failingResponseBody) Read([]byte) (int, error) { return 0, r.err }
func (failingResponseBody) Close() error               { return nil }

func TestResponseBodyFailureClassificationAndRouting(t *testing.T) {
	constructors := map[string]func(*http.Client, string, string, time.Duration) provider.Provider{
		"openai": func(c *http.Client, key, url string, timeout time.Duration) provider.Provider {
			return openaiadapter.New(c, key, url, timeout)
		},
		"anthropic": func(c *http.Client, key, url string, timeout time.Duration) provider.Provider {
			return anthropic.New(c, key, url, timeout)
		},
	}
	for name, newProvider := range constructors {
		for _, test := range []struct {
			name     string
			body     func() io.ReadCloser
			kind     provider.ErrorKind
			fallback bool
		}{
			{"timeout", func() io.ReadCloser { return failingResponseBody{context.DeadlineExceeded} }, provider.ErrorTimeout, true},
			{"network", func() io.ReadCloser { return failingResponseBody{errors.New("private transport diagnostic")} }, provider.ErrorUnavailable, true},
			{"truncated", func() io.ReadCloser { return failingResponseBody{io.ErrUnexpectedEOF} }, provider.ErrorUnavailable, true},
			{"protocol", func() io.ReadCloser {
				return failingResponseBody{&http.ProtocolError{ErrorString: "private transport diagnostic"}}
			}, provider.ErrorInternal, false},
			{"malformed", func() io.ReadCloser { return io.NopCloser(strings.NewReader("{")) }, provider.ErrorInternal, false},
			{"oversized", func() io.ReadCloser { return io.NopCloser(strings.NewReader(strings.Repeat("x", (2<<20)+1))) }, provider.ErrorInternal, false},
		} {
			t.Run(name+"/"+test.name, func(t *testing.T) {
				p := newProvider(&http.Client{Transport: bodyTransport{test.body}}, "", "http://unused", time.Second)
				_, err := p.Complete(context.Background(), validRequest())
				var typed *provider.Error
				if !errors.As(err, &typed) || typed.Kind != test.kind {
					t.Fatalf("classification = %v, want %s", err, test.kind)
				}
				if strings.Contains(err.Error(), "private transport diagnostic") {
					t.Fatal("raw transport error exposed")
				}
				fallback := &recordingProvider{name: "fallback"}
				resolver := model.New(map[string]map[string]string{model.General: {name: "test-model", "fallback": "test-model"}})
				service := NewAutoWithCircuitBreaker(resolver, CircuitConfig{FailureThreshold: 1, OpenDuration: time.Minute}, p, fallback)
				request := validRequest()
				request.Model = model.General
				_, err = service.Complete(context.Background(), request)
				if test.fallback {
					if err != nil || fallback.calls != 1 {
						t.Fatalf("fallback failed: error=%v calls=%d", err, fallback.calls)
					}
					assertProviderHealth(t, service, name, circuitOpen, 1)
				} else {
					if err == nil || fallback.calls != 0 {
						t.Fatal("invalid response triggered fallback")
					}
					assertProviderHealth(t, service, name, circuitClosed, 0)
				}
			})
		}
	}
}
