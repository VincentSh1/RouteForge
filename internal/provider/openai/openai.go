package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	common "github.com/VincentSh1/RouteForge/internal/openai"
	providerpkg "github.com/VincentSh1/RouteForge/internal/provider"
)

const (
	Name            = "openai"
	DefaultBaseURL  = "https://api.openai.com"
	maxResponseSize = 2 << 20
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
	payload := request{Model: req.Model, Messages: make([]message, len(req.Messages))}
	for i, item := range req.Messages {
		payload.Messages[i] = message{Role: item.Role, Content: item.Content}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return common.ChatCompletionResponse{}, providerpkg.NewError(providerpkg.ErrorInternal, Name, errors.New("encode request"))
	}
	upstreamReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return common.ChatCompletionResponse{}, providerpkg.NewError(providerpkg.ErrorInternal, Name, errors.New("create request"))
	}
	upstreamReq.Header.Set("Authorization", "Bearer "+p.apiKey)
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

	return common.ChatCompletionResponse{
		ID:      decoded.ID,
		Object:  decoded.Object,
		Created: decoded.Created,
		Model:   decoded.Model,
		Choices: choices,
		Usage: common.Usage{
			PromptTokens:     decoded.Usage.PromptTokens,
			CompletionTokens: decoded.Usage.CompletionTokens,
			TotalTokens:      decoded.Usage.TotalTokens,
		},
	}, nil
}

type request struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
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
	Usage   usage    `json:"usage"`
}

type choice struct {
	Index        int     `json:"index"`
	Message      message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}
