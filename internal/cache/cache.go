// Package cache defines transient completion storage. Values contain generated
// content and must never be logged or included in operational history.
package cache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/VincentSh1/RouteForge/internal/openai"
)

const (
	MaxValueBytes    = 1 << 20
	OperationTimeout = 50 * time.Millisecond
	DefaultTTL       = 5 * time.Minute
)

type Store interface {
	Enabled() bool
	Get(context.Context, string) ([]byte, error)
	Set(context.Context, string, []byte, time.Duration) error
}

type Noop struct{}

func (Noop) Enabled() bool                                            { return false }
func (Noop) Get(context.Context, string) ([]byte, error)              { return nil, nil }
func (Noop) Set(context.Context, string, []byte, time.Duration) error { return nil }

// Key includes the complete supported request so newly supported fields cannot
// accidentally share entries with a different generation configuration.
func Key(request openai.ChatCompletionRequest, provider, resolvedModel string) (string, error) {
	hash := sha256.New()
	err := json.NewEncoder(hash).Encode(struct {
		Request       openai.ChatCompletionRequest `json:"request"`
		Provider      string                       `json:"provider"`
		ResolvedModel string                       `json:"resolved_model"`
	}{request, provider, resolvedModel})
	if err != nil {
		return "", errors.New("cache key encoding failed")
	}
	return "routeforge:chat:v1:" + hex.EncodeToString(hash.Sum(nil)), nil
}

type envelope struct {
	Version  int                           `json:"version"`
	Response openai.ChatCompletionResponse `json:"response"`
}

// Only normal, complete assistant answers are reused. In particular, an output
// cut short by a token limit or any unsupported finish reason is not cached.
func complete(response openai.ChatCompletionResponse) bool {
	if response.ID == "" || response.Object != "chat.completion" || response.Model == "" || len(response.Choices) == 0 {
		return false
	}
	for i, choice := range response.Choices {
		if choice.Index != i || choice.Message.Role != "assistant" || choice.Message.Content == "" || choice.FinishReason != "stop" {
			return false
		}
	}
	return true
}

type boundedBuffer struct{ bytes.Buffer }

func (b *boundedBuffer) Write(data []byte) (int, error) {
	if len(data) > MaxValueBytes-b.Len() {
		return 0, errors.New("cache value exceeds size limit")
	}
	return b.Buffer.Write(data)
}

func Encode(response openai.ChatCompletionResponse) ([]byte, error) {
	if !complete(response) {
		return nil, errors.New("completion is not cacheable")
	}
	var buffer boundedBuffer
	if err := json.NewEncoder(&buffer).Encode(envelope{1, response}); err != nil {
		return nil, errors.New("cache value encoding failed")
	}
	return buffer.Bytes(), nil
}

func Decode(data []byte) (openai.ChatCompletionResponse, error) {
	var value envelope
	if len(data) == 0 || len(data) > MaxValueBytes {
		return value.Response, errors.New("invalid cache value size")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return openai.ChatCompletionResponse{}, errors.New("invalid cache value")
	}
	if value.Version != 1 || !complete(value.Response) || decoder.Decode(new(any)) != io.EOF {
		return openai.ChatCompletionResponse{}, errors.New("invalid cache completion")
	}
	return value.Response, nil
}
