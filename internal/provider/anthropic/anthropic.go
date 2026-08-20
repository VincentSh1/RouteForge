package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	client  *http.Client
	apiKey  string
	baseURL string
}

func New(client *http.Client, apiKey, baseURL string) *Provider {
	return &Provider{client: providerpkg.HTTPClientWithoutRedirects(client), apiKey: apiKey, baseURL: strings.TrimRight(baseURL, "/")}
}

func (p *Provider) Name() string { return Name }

func (p *Provider) Complete(ctx context.Context, req common.ChatCompletionRequest) (common.ChatCompletionResponse, error) {
	payload := request{Model: req.Model, MaxTokens: defaultMaxTokens}
	var systemMessages []string
	for _, item := range req.Messages {
		if item.Role == "system" {
			systemMessages = append(systemMessages, item.Content)
			continue
		}
		payload.Messages = append(payload.Messages, message{Role: item.Role, Content: item.Content})
	}
	if len(payload.Messages) == 0 {
		return common.ChatCompletionResponse{}, providerpkg.NewError(providerpkg.ErrorInvalidRequest, Name, errors.New("at least one non-system message is required"))
	}
	payload.System = strings.Join(systemMessages, "\n\n")

	body, err := json.Marshal(payload)
	if err != nil {
		return common.ChatCompletionResponse{}, providerpkg.NewError(providerpkg.ErrorInternal, Name, errors.New("encode request"))
	}
	upstreamReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return common.ChatCompletionResponse{}, providerpkg.NewError(providerpkg.ErrorInternal, Name, errors.New("create request"))
	}
	upstreamReq.Header.Set("X-Api-Key", p.apiKey)
	upstreamReq.Header.Set("Anthropic-Version", apiVersion)
	upstreamReq.Header.Set("Content-Type", "application/json")

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
		Usage: common.Usage{
			PromptTokens:     decoded.Usage.InputTokens,
			CompletionTokens: decoded.Usage.OutputTokens,
			TotalTokens:      decoded.Usage.InputTokens + decoded.Usage.OutputTokens,
		},
	}, nil
}

type request struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system,omitempty"`
	Messages  []message `json:"messages"`
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
	Usage      usage          `json:"usage"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}
