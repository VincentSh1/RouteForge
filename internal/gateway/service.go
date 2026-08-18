package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/VincentSh1/RouteForge/internal/openai"
	"github.com/VincentSh1/RouteForge/internal/provider"
)

var (
	ErrModelRequired        = errors.New("model is required")
	ErrMessagesRequired     = errors.New("at least one message is required")
	ErrStreamingUnsupported = errors.New("streaming is not supported")
)

type UnsupportedRoleError struct {
	Index int
	Role  string
}

func (e *UnsupportedRoleError) Error() string {
	return fmt.Sprintf("messages[%d].role %q is not supported", e.Index, e.Role)
}

// Service owns completion validation and provider delegation. Routing policy
// can be added here later without changing the HTTP transport.
type Service struct {
	provider provider.Provider
}

func New(p provider.Provider) *Service { return &Service{provider: p} }

func (s *Service) Complete(ctx context.Context, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if strings.TrimSpace(req.Model) == "" {
		return openai.ChatCompletionResponse{}, ErrModelRequired
	}
	if len(req.Messages) == 0 {
		return openai.ChatCompletionResponse{}, ErrMessagesRequired
	}
	if req.Stream {
		return openai.ChatCompletionResponse{}, ErrStreamingUnsupported
	}
	for i, message := range req.Messages {
		switch message.Role {
		case "system", "user", "assistant":
		default:
			return openai.ChatCompletionResponse{}, &UnsupportedRoleError{Index: i, Role: message.Role}
		}
	}

	return s.provider.Complete(ctx, req)
}
