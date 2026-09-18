package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/VincentSh1/RouteForge/internal/openai"
	"github.com/VincentSh1/RouteForge/internal/provider"
)

type closeTrackingProvider struct {
	*streamingTestProvider
	closes int
}

func (p *closeTrackingProvider) Stream(ctx context.Context, req openai.ChatCompletionRequest) (provider.Stream, error) {
	stream, err := p.streamingTestProvider.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	return &closeTrackingStream{Stream: stream, closes: &p.closes}, nil
}

type closeTrackingStream struct {
	provider.Stream
	closes *int
}

func (s *closeTrackingStream) Close() error {
	*s.closes++
	return s.Stream.Close()
}

func TestStreamTerminalBookkeepingExactlyOnce(t *testing.T) {
	for _, test := range []struct {
		name      string
		terminal  error
		emitError error
		outcome   string
	}{
		{"EOF", nil, nil, "success"},
		{"wrapped EOF", fmt.Errorf("stream ended: %w", io.EOF), nil, "success"},
		{"upstream failure", provider.NewError(provider.ErrorUnavailable, "first", errors.New("unavailable")), nil, "unavailable"},
		{"downstream failure", nil, errors.New("downstream closed"), "cancellation"},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := &closeTrackingProvider{streamingTestProvider: &streamingTestProvider{
				name: "first", chunks: []provider.StreamChunk{{Content: "synthetic", Usage: openai.NewUsage(2, 3, 5)}}, err: test.terminal, errAfter: 1,
			}}
			service := New(upstream, testResolver())
			recorder := &capturingHistoryRecorder{}
			attachHistory(service, recorder)
			err := service.Stream(context.Background(), validRequest(), func(chunk provider.StreamChunk) error {
				if chunk.Usage != nil {
					t.Fatal("internal usage exposed")
				}
				return test.emitError
			})
			if (err == nil) != (test.outcome == "success") {
				t.Fatal("terminal result changed")
			}
			if upstream.closes != 1 {
				t.Fatal("stream not closed exactly once")
			}
			record := recorder.record(t)
			if len(record.Attempts) != 1 || record.Attempts[0].Outcome != test.outcome || record.Attempts[0].TTFCUS == nil || value(record.Attempts[0].TotalTokens) != 5 {
				t.Fatal("terminal history changed")
			}
			telemetry, _ := service.telemetry.snapshot("first")
			if telemetry.Attempts != 1 || telemetry.Successes+telemetry.Failures+telemetry.Cancellations != 1 || len(telemetry.StreamingDurations) != 1 {
				t.Fatal("terminal telemetry not recorded exactly once")
			}
		})
	}
}
