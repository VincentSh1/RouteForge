package cache

import (
	"reflect"
	"strings"
	"testing"

	"github.com/VincentSh1/RouteForge/internal/openai"
)

func completion() openai.ChatCompletionResponse {
	return openai.ChatCompletionResponse{ID: "synthetic", Object: "chat.completion", Model: "model", Choices: []openai.Choice{{Message: openai.Message{Role: "assistant", Content: "synthetic answer"}, FinishReason: "stop"}}, Usage: openai.NewUsage(2, 3, 5)}
}

func TestCanonicalKeyIncludesEverySupportedField(t *testing.T) {
	req := openai.ChatCompletionRequest{Model: "routeforge/general", Messages: []openai.Message{{Role: "user", Content: "private synthetic prompt"}}}
	key, err := Key(req, "first", "native")
	if err != nil || len(key) != len("routeforge:chat:v1:")+64 || strings.Contains(key, "private") {
		t.Fatal("invalid hashed key")
	}
	copyKey, _ := Key(req, "first", "native")
	if copyKey != key {
		t.Fatal("key is not deterministic")
	}
	variants := []struct {
		request         openai.ChatCompletionRequest
		provider, model string
	}{
		{req, "second", "native"}, {req, "first", "other"},
		{openai.ChatCompletionRequest{Model: "other", Messages: req.Messages}, "first", "native"},
		{openai.ChatCompletionRequest{Model: req.Model, Messages: req.Messages, Stream: true}, "first", "native"},
		{openai.ChatCompletionRequest{Model: req.Model, Messages: []openai.Message{{Role: "system", Content: req.Messages[0].Content}}}, "first", "native"},
		{openai.ChatCompletionRequest{Model: req.Model, Messages: []openai.Message{{Role: "user", Content: "different"}}}, "first", "native"},
	}
	for _, variant := range variants {
		other, _ := Key(variant.request, variant.provider, variant.model)
		if other == key {
			t.Fatal("request context missing from key")
		}
	}
}

func TestValueRoundTripAndBounds(t *testing.T) {
	response := completion()
	data, err := Encode(response)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(data)
	if err != nil || !reflect.DeepEqual(decoded, response) {
		t.Fatal("response did not round trip")
	}
	for _, invalid := range [][]byte{nil, []byte(`{`), []byte(`{"version":2}`), append(data, []byte(`{}`)...), []byte(strings.Repeat("x", MaxValueBytes+1)), []byte(strings.Replace(string(data), `"version":1`, `"unknown":true,"version":1`, 1))} {
		if _, err := Decode(invalid); err == nil {
			t.Fatal("invalid value accepted")
		}
	}
	response.Choices[0].Message.Content = strings.Repeat("x", MaxValueBytes)
	if _, err := Encode(response); err == nil {
		t.Fatal("oversized value accepted")
	}
	response = completion()
	response.Choices[0].FinishReason = "length"
	if _, err := Encode(response); err == nil {
		t.Fatal("partial response accepted")
	}
}

func TestRedisURLValidation(t *testing.T) {
	for _, raw := range []string{"redis://localhost:6379", "rediss://localhost:6379/0"} {
		if err := ValidateURL(raw); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{"", "https://localhost", "redis:///0", "redis://localhost:-1", "redis://localhost/invalid", "redis://localhost?max_retries=99"} {
		if err := ValidateURL(raw); err == nil {
			t.Fatal("invalid URL accepted")
		}
	}
}
