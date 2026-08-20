package provider

import (
	"context"
	"testing"

	"github.com/VincentSh1/RouteForge/internal/openai"
)

type namedProvider string

func (p namedProvider) Name() string { return string(p) }

func (p namedProvider) Complete(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{}, nil
}

func TestRegistrySelectsExplicitProviders(t *testing.T) {
	registry := NewRegistry(namedProvider("mock"), namedProvider("openai"), namedProvider("anthropic"))
	for _, name := range []string{"mock", "openai", "anthropic"} {
		selected, ok := registry.Get(name)
		if !ok || selected.Name() != name {
			t.Fatalf("Get(%q) = %v, %t", name, selected, ok)
		}
	}
	if _, ok := registry.Get("missing"); ok {
		t.Fatal("Get(missing) found an unregistered provider")
	}
}
