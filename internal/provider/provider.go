package provider

import (
	"context"
	"fmt"

	"github.com/VincentSh1/RouteForge/internal/openai"
)

// Provider performs chat completions against an inference backend.
type Provider interface {
	Name() string
	Complete(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error)
}

type StreamingProvider interface {
	Provider
	Stream(context.Context, openai.ChatCompletionRequest) (Stream, error)
}

type Stream interface {
	Next() (StreamChunk, error)
	Close() error
}

type StreamChunk struct {
	ID           string
	Created      int64
	Model        string
	Role         string
	Content      string
	FinishReason string
}

type ErrorKind string

const (
	ErrorInvalidRequest ErrorKind = "invalid_request"
	ErrorUnavailable    ErrorKind = "unavailable"
	ErrorTimeout        ErrorKind = "timeout"
	ErrorRateLimited    ErrorKind = "rate_limited"
	ErrorInternal       ErrorKind = "internal"
)

// Error classifies provider failures without coupling providers to HTTP.
type Error struct {
	Kind     ErrorKind
	Provider string
	Err      error
}

func (e *Error) Error() string {
	if e.Provider == "" {
		return fmt.Sprintf("provider %s: %v", e.Kind, e.Err)
	}
	return fmt.Sprintf("provider %s %s: %v", e.Provider, e.Kind, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

func NewError(kind ErrorKind, providerName string, err error) error {
	return &Error{Kind: kind, Provider: providerName, Err: err}
}
