package model

import (
	"errors"
	"testing"
)

func TestResolverResolvesProviderSpecificModels(t *testing.T) {
	resolver := New(map[string]map[string]string{
		General: {
			"openai":    "openai-model",
			"anthropic": "anthropic-model",
		},
	})

	for _, test := range []struct {
		provider string
		want     string
	}{
		{provider: "openai", want: "openai-model"},
		{provider: "anthropic", want: "anthropic-model"},
	} {
		got, err := resolver.Resolve(General, test.provider)
		if err != nil {
			t.Fatalf("Resolve(%q) error = %v", test.provider, err)
		}
		if got != test.want {
			t.Fatalf("Resolve(%q) = %q, want %q", test.provider, got, test.want)
		}
	}
}

func TestResolverRejectsUnknownAlias(t *testing.T) {
	_, err := New(nil).Resolve("routeforge/unknown", "openai")
	assertResolutionError(t, err, ErrorUnknownAlias)
}

func TestResolverRejectsMissingProviderMapping(t *testing.T) {
	resolver := New(map[string]map[string]string{General: {"openai": "openai-model"}})
	_, err := resolver.Resolve(General, "anthropic")
	assertResolutionError(t, err, ErrorMissingMapping)
}

func TestIsLogical(t *testing.T) {
	if !IsLogical(General) {
		t.Fatalf("IsLogical(%q) = false", General)
	}
	if IsLogical("provider-native-model") {
		t.Fatal("provider-native model was classified as logical")
	}
}

func assertResolutionError(t *testing.T, err error, want ErrorKind) {
	t.Helper()
	var resolutionErr *ResolutionError
	if !errors.As(err, &resolutionErr) {
		t.Fatalf("error = %v, want ResolutionError", err)
	}
	if resolutionErr.Kind != want {
		t.Fatalf("error kind = %q, want %q", resolutionErr.Kind, want)
	}
}
