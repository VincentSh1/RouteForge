package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	common "github.com/VincentSh1/RouteForge/internal/openai"
	providerpkg "github.com/VincentSh1/RouteForge/internal/provider"
)

const (
	Name            = "openai"
	DefaultBaseURL  = "https://api.openai.com"
	maxResponseSize = 2 << 20
)

type Provider struct {
	client            *http.Client
	streamClient      *http.Client
	apiKey            string
	baseURL           string
	streamIdleTimeout time.Duration
}

func New(client *http.Client, apiKey, baseURL string, streamIdleTimeout time.Duration) *Provider {
	return &Provider{
		client:            providerpkg.HTTPClientWithoutRedirects(client),
		streamClient:      providerpkg.HTTPClientForStreaming(client),
		apiKey:            apiKey,
		baseURL:           strings.TrimRight(baseURL, "/"),
		streamIdleTimeout: streamIdleTimeout,
	}
}

func (p *Provider) Name() string { return Name }

func (p *Provider) Complete(ctx context.Context, req common.ChatCompletionRequest) (common.ChatCompletionResponse, error) {
	upstreamReq, err := p.upstreamRequest(ctx, req, false)
	if err != nil {
		return common.ChatCompletionResponse{}, err
	}

	resp, err := p.client.Do(upstreamReq)
	if err != nil {
		return common.ChatCompletionResponse{}, providerpkg.TransportError(Name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return common.ChatCompletionResponse{}, providerpkg.HTTPStatusError(Name, resp.StatusCode)
	}

	responseBody, err := providerpkg.ReadResponse(resp.Body, maxResponseSize)
	if err != nil {
		return common.ChatCompletionResponse{}, providerpkg.NewError(providerpkg.ErrorInternal, Name, err)
	}
	var decoded response
	if err := json.Unmarshal(responseBody, &decoded); err != nil || len(decoded.Choices) == 0 {
		return common.ChatCompletionResponse{}, providerpkg.NewError(providerpkg.ErrorInternal, Name, errors.New("malformed upstream response"))
	}

	choices := make([]common.Choice, len(decoded.Choices))
	for i, item := range decoded.Choices {
		choices[i] = common.Choice{
			Index:        item.Index,
			Message:      common.Message{Role: item.Message.Role, Content: item.Message.Content},
			FinishReason: item.FinishReason,
		}
	}

	providerUsage, err := normalizeUsage(decoded.Usage)
	if err != nil {
		return common.ChatCompletionResponse{}, providerpkg.NewError(providerpkg.ErrorInternal, Name, err)
	}

	return common.ChatCompletionResponse{
		ID:      decoded.ID,
		Object:  decoded.Object,
		Created: decoded.Created,
		Model:   decoded.Model,
		Choices: choices,
		Usage:   providerUsage,
	}, nil
}

func (p *Provider) Stream(ctx context.Context, req common.ChatCompletionRequest) (providerpkg.Stream, error) {
	streamCtx, watchdog := providerpkg.NewStreamIdleWatchdog(ctx, p.streamIdleTimeout)
	upstreamReq, err := p.upstreamRequest(streamCtx, req, true)
	if err != nil {
		watchdog.Stop()
		return nil, err
	}
	resp, err := p.streamClient.Do(upstreamReq)
	if err != nil {
		watchdog.Stop()
		return nil, providerpkg.TransportError(Name, err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_ = resp.Body.Close()
		watchdog.Stop()
		return nil, providerpkg.HTTPStatusError(Name, resp.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "text/event-stream" {
		_ = resp.Body.Close()
		watchdog.Stop()
		return nil, providerpkg.NewError(providerpkg.ErrorInternal, Name, errors.New("unexpected upstream content type"))
	}
	body := watchdog.Wrap(resp.Body)
	return &stream{ctx: streamCtx, body: body, events: providerpkg.NewSSEReader(body), watchdog: watchdog}, nil
}

func (p *Provider) upstreamRequest(ctx context.Context, req common.ChatCompletionRequest, stream bool) (*http.Request, error) {
	payload := request{Model: req.Model, Messages: make([]message, len(req.Messages)), Stream: stream}
	if stream {
		payload.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	for i, item := range req.Messages {
		payload.Messages[i] = message{Role: item.Role, Content: item.Content}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, providerpkg.NewError(providerpkg.ErrorInternal, Name, errors.New("encode request"))
	}
	upstreamReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, providerpkg.NewError(providerpkg.ErrorInternal, Name, errors.New("create request"))
	}
	upstreamReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	upstreamReq.Header.Set("Content-Type", "application/json")
	return upstreamReq, nil
}

type stream struct {
	ctx      context.Context
	body     io.ReadCloser
	events   *providerpkg.SSEReader
	watchdog *providerpkg.StreamIdleWatchdog
	done     bool
}

func (s *stream) Next() (providerpkg.StreamChunk, error) {
	if s.done {
		return providerpkg.StreamChunk{}, io.EOF
	}
	for {
		event, err := s.events.Next()
		if err != nil {
			if s.ctx.Err() != nil {
				return providerpkg.StreamChunk{}, providerpkg.TransportError(Name, s.ctx.Err())
			}
			if errors.Is(err, io.EOF) || errors.Is(err, providerpkg.ErrSSERead) {
				return providerpkg.StreamChunk{}, providerpkg.NewError(providerpkg.ErrorUnavailable, Name, errors.New("upstream stream ended unexpectedly"))
			}
			return providerpkg.StreamChunk{}, providerpkg.NewError(providerpkg.ErrorInternal, Name, errors.New("incomplete upstream stream"))
		}
		if string(event.Data) == "[DONE]" {
			s.done = true
			return providerpkg.StreamChunk{}, io.EOF
		}
		if len(event.Data) == 0 {
			continue
		}
		var decoded streamResponse
		if err := json.Unmarshal(event.Data, &decoded); err != nil {
			return providerpkg.StreamChunk{}, providerpkg.NewError(providerpkg.ErrorInternal, Name, errors.New("malformed upstream stream event"))
		}
		providerUsage, err := normalizeUsage(decoded.Usage)
		if err != nil {
			return providerpkg.StreamChunk{}, providerpkg.NewError(providerpkg.ErrorInternal, Name, err)
		}
		if len(decoded.Choices) == 0 {
			if providerUsage != nil {
				return providerpkg.StreamChunk{Usage: providerUsage}, nil
			}
			return providerpkg.StreamChunk{}, providerpkg.NewError(providerpkg.ErrorInternal, Name, errors.New("malformed upstream stream event"))
		}
		choice := decoded.Choices[0]
		finishReason := ""
		if choice.FinishReason != nil {
			finishReason = *choice.FinishReason
		}
		return providerpkg.StreamChunk{
			ID: decoded.ID, Created: decoded.Created, Model: decoded.Model,
			Role: choice.Delta.Role, Content: choice.Delta.Content, FinishReason: finishReason, Usage: providerUsage,
		}, nil
	}
}

func (s *stream) Close() error {
	s.watchdog.Stop()
	return s.body.Close()
}

type request struct {
	Model         string         `json:"model"`
	Messages      []message      `json:"messages"`
	Stream        bool           `json:"stream,omitempty"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type streamResponse struct {
	ID      string         `json:"id"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []streamChoice `json:"choices"`
	Usage   *usage         `json:"usage"`
}

type streamChoice struct {
	Delta        streamDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type streamDelta struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type response struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []choice `json:"choices"`
	Usage   *usage   `json:"usage"`
}

type choice struct {
	Index        int     `json:"index"`
	Message      message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

type usage struct {
	PromptTokens     *uint64 `json:"prompt_tokens"`
	CompletionTokens *uint64 `json:"completion_tokens"`
	TotalTokens      *uint64 `json:"total_tokens"`
}

func normalizeUsage(upstream *usage) (*common.Usage, error) {
	if upstream == nil || upstream.PromptTokens == nil && upstream.CompletionTokens == nil && upstream.TotalTokens == nil {
		return nil, nil
	}
	if upstream.PromptTokens == nil || upstream.CompletionTokens == nil || upstream.TotalTokens == nil {
		return nil, errors.New("malformed upstream usage")
	}
	if *upstream.PromptTokens > ^uint64(0)-*upstream.CompletionTokens ||
		*upstream.TotalTokens != *upstream.PromptTokens+*upstream.CompletionTokens {
		return nil, errors.New("malformed upstream usage")
	}
	return common.NewUsage(*upstream.PromptTokens, *upstream.CompletionTokens, *upstream.TotalTokens), nil
}
