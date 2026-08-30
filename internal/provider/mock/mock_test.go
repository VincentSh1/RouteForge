package mock

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/VincentSh1/RouteForge/internal/openai"
)

func TestCompleteReportsDeterministicUsage(t *testing.T) {
	response, err := (&Provider{}).Complete(context.Background(), openai.ChatCompletionRequest{Model: "mock-model"})
	if err != nil || response.Usage == nil || response.Usage.InputTokens == nil || *response.Usage.InputTokens != 3 ||
		response.Usage.OutputTokens == nil || *response.Usage.OutputTokens != 4 ||
		response.Usage.TotalTokens == nil || *response.Usage.TotalTokens != 7 {
		t.Fatalf("response = %+v, error = %v", response, err)
	}
}

func TestStreamEmitsDeterministicChunks(t *testing.T) {
	stream, err := (&Provider{}).Stream(context.Background(), openai.ChatCompletionRequest{Model: "mock-model"})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	defer stream.Close()
	var contents []string
	finished := false
	usageReported := false
	for {
		chunk, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next() error = %v", err)
		}
		if chunk.Content != "" {
			contents = append(contents, chunk.Content)
		}
		if chunk.FinishReason == "stop" {
			finished = true
		}
		if chunk.Usage != nil {
			usageReported = true
		}
	}
	want := []string{"Hello", " from", " RouteForge."}
	if len(contents) != len(want) {
		t.Fatalf("contents = %q", contents)
	}
	for i := range want {
		if contents[i] != want[i] {
			t.Fatalf("contents = %q", contents)
		}
	}
	if !finished || !usageReported {
		t.Fatal("stream did not emit successful completion")
	}
}

func TestStreamFailureBeforeFirstChunk(t *testing.T) {
	want := errors.New("failed")
	_, err := (&Provider{StreamErr: want}).Stream(context.Background(), openai.ChatCompletionRequest{})
	if !errors.Is(err, want) {
		t.Fatalf("Stream() error = %v", err)
	}
}

func TestStreamFailureAfterChunk(t *testing.T) {
	want := errors.New("failed")
	stream, err := (&Provider{StreamChunks: []string{"first", "second"}, StreamErr: want, StreamErrAfter: 1}).Stream(context.Background(), openai.ChatCompletionRequest{})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	if _, err := stream.Next(); err != nil {
		t.Fatalf("first Next() error = %v", err)
	}
	if _, err := stream.Next(); !errors.Is(err, want) {
		t.Fatalf("second Next() error = %v", err)
	}
}
