package anthropic

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
	Name             = "anthropic"
	DefaultBaseURL   = "https://api.anthropic.com"
	apiVersion       = "2023-06-01"
	defaultMaxTokens = 1024
	maxResponseSize  = 2 << 20
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
	if err := json.Unmarshal(responseBody, &decoded); err != nil || decoded.ID == "" {
		return common.ChatCompletionResponse{}, providerpkg.NewError(providerpkg.ErrorInternal, Name, errors.New("malformed upstream response"))
	}

	var content strings.Builder
	for _, block := range decoded.Content {
		if block.Type == "text" {
			content.WriteString(block.Text)
		}
	}
	finishReason := "stop"
	if decoded.StopReason == "max_tokens" {
		finishReason = "length"
	}

	providerUsage, err := normalizeCompleteUsage(decoded.Usage)
	if err != nil {
		return common.ChatCompletionResponse{}, providerpkg.NewError(providerpkg.ErrorInternal, Name, err)
	}

	return common.ChatCompletionResponse{
		ID:      decoded.ID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   decoded.Model,
		Choices: []common.Choice{{
			Index:        0,
			Message:      common.Message{Role: "assistant", Content: content.String()},
			FinishReason: finishReason,
		}},
		Usage: providerUsage,
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
	payload := request{Model: req.Model, MaxTokens: defaultMaxTokens, Stream: stream}
	var systemMessages []string
	for _, item := range req.Messages {
		if item.Role == "system" {
			systemMessages = append(systemMessages, item.Content)
			continue
		}
		payload.Messages = append(payload.Messages, message{Role: item.Role, Content: item.Content})
	}
	if len(payload.Messages) == 0 {
		return nil, providerpkg.NewError(providerpkg.ErrorInvalidRequest, Name, errors.New("at least one non-system message is required"))
	}
	payload.System = strings.Join(systemMessages, "\n\n")
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, providerpkg.NewError(providerpkg.ErrorInternal, Name, errors.New("encode request"))
	}
	upstreamReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, providerpkg.NewError(providerpkg.ErrorInternal, Name, errors.New("create request"))
	}
	upstreamReq.Header.Set("X-Api-Key", p.apiKey)
	upstreamReq.Header.Set("Anthropic-Version", apiVersion)
	upstreamReq.Header.Set("Content-Type", "application/json")
	return upstreamReq, nil
}

type stream struct {
	ctx      context.Context
	body     io.ReadCloser
	events   *providerpkg.SSEReader
	watchdog *providerpkg.StreamIdleWatchdog
	id       string
	model    string
	sentRole bool
	done     bool
	usage    *common.Usage
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
		var decoded streamEvent
		if len(event.Data) > 0 {
			if err := json.Unmarshal(event.Data, &decoded); err != nil {
				return providerpkg.StreamChunk{}, providerpkg.NewError(providerpkg.ErrorInternal, Name, errors.New("malformed upstream stream event"))
			}
		}
		eventType := event.Type
		if eventType == "" {
			eventType = decoded.Type
		}
		switch eventType {
		case "ping", "content_block_stop":
			continue
		case "message_start":
			s.id = decoded.Message.ID
			s.model = decoded.Message.Model
			providerUsage, err := mergeUsage(s.usage, decoded.Message.Usage)
			if err != nil {
				return providerpkg.StreamChunk{}, providerpkg.NewError(providerpkg.ErrorInternal, Name, err)
			}
			s.usage = providerUsage
			if providerUsage != nil {
				return providerpkg.StreamChunk{Usage: providerUsage}, nil
			}
			continue
		case "content_block_start":
			if decoded.ContentBlock.Type != "text" {
				return providerpkg.StreamChunk{}, providerpkg.NewError(providerpkg.ErrorInternal, Name, errors.New("unsupported upstream content block"))
			}
			if decoded.ContentBlock.Text == "" {
				continue
			}
			return s.textChunk(decoded.ContentBlock.Text), nil
		case "content_block_delta":
			if decoded.Delta.Type != "text_delta" {
				return providerpkg.StreamChunk{}, providerpkg.NewError(providerpkg.ErrorInternal, Name, errors.New("unsupported upstream content delta"))
			}
			return s.textChunk(decoded.Delta.Text), nil
		case "message_delta":
			providerUsage, err := mergeUsage(s.usage, decoded.Usage)
			if err != nil {
				return providerpkg.StreamChunk{}, providerpkg.NewError(providerpkg.ErrorInternal, Name, err)
			}
			s.usage = providerUsage
			if decoded.Delta.StopReason == "" {
				if decoded.Usage != nil {
					return providerpkg.StreamChunk{Usage: providerUsage}, nil
				}
				continue
			}
			finishReason := "stop"
			if decoded.Delta.StopReason == "max_tokens" {
				finishReason = "length"
			}
			return providerpkg.StreamChunk{
				ID: s.id, Created: time.Now().Unix(), Model: s.model,
				FinishReason: finishReason, Usage: providerUsage,
			}, nil
		case "message_stop":
			s.done = true
			return providerpkg.StreamChunk{}, io.EOF
		default:
			return providerpkg.StreamChunk{}, providerpkg.NewError(providerpkg.ErrorInternal, Name, errors.New("unsupported upstream stream event"))
		}
	}
}

func (s *stream) textChunk(content string) providerpkg.StreamChunk {
	chunk := providerpkg.StreamChunk{ID: s.id, Created: time.Now().Unix(), Model: s.model, Content: content}
	if !s.sentRole {
		chunk.Role = "assistant"
		s.sentRole = true
	}
	return chunk
}

func (s *stream) Close() error {
	s.watchdog.Stop()
	return s.body.Close()
}

type request struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system,omitempty"`
	Messages  []message `json:"messages"`
	Stream    bool      `json:"stream,omitempty"`
}

type streamEvent struct {
	Type    string `json:"type"`
	Message struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *usage `json:"usage"`
	} `json:"message"`
	ContentBlock contentBlock `json:"content_block"`
	Delta        struct {
		Type       string `json:"type"`
		Text       string `json:"text"`
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	Usage *usage `json:"usage"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type response struct {
	ID         string         `json:"id"`
	Model      string         `json:"model"`
	Content    []contentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	Usage      *usage         `json:"usage"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type usage struct {
	InputTokens  *uint64 `json:"input_tokens"`
	OutputTokens *uint64 `json:"output_tokens"`
}

func normalizeCompleteUsage(upstream *usage) (*common.Usage, error) {
	if upstream == nil || upstream.InputTokens == nil && upstream.OutputTokens == nil {
		return nil, nil
	}
	if upstream.InputTokens == nil || upstream.OutputTokens == nil {
		return nil, errors.New("malformed upstream usage")
	}
	if *upstream.InputTokens > ^uint64(0)-*upstream.OutputTokens {
		return nil, errors.New("malformed upstream usage")
	}
	return common.NewUsage(
		*upstream.InputTokens,
		*upstream.OutputTokens,
		*upstream.InputTokens+*upstream.OutputTokens,
	), nil
}

func mergeUsage(current *common.Usage, upstream *usage) (*common.Usage, error) {
	if upstream == nil || upstream.InputTokens == nil && upstream.OutputTokens == nil {
		return current, nil
	}
	merged := &common.Usage{}
	if current != nil {
		merged.InputTokens = current.InputTokens
		merged.OutputTokens = current.OutputTokens
		merged.TotalTokens = current.TotalTokens
	}
	if upstream.InputTokens != nil {
		input := *upstream.InputTokens
		merged.InputTokens = &input
	}
	if upstream.OutputTokens != nil {
		output := *upstream.OutputTokens
		merged.OutputTokens = &output
	}
	if merged.InputTokens != nil && merged.OutputTokens != nil {
		if *merged.InputTokens > ^uint64(0)-*merged.OutputTokens {
			return nil, errors.New("malformed upstream usage")
		}
		total := *merged.InputTokens + *merged.OutputTokens
		merged.TotalTokens = &total
	}
	return merged, nil
}
