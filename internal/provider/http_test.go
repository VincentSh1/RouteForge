package provider

import (
	"net/http"
	"testing"
	"time"
)

func TestHTTPClientWithoutRedirectsPreservesSettings(t *testing.T) {
	original := &http.Client{Timeout: 3 * time.Second}
	client := HTTPClientWithoutRedirects(original)
	if client == original {
		t.Fatal("client was not cloned")
	}
	if client.Timeout != original.Timeout {
		t.Fatalf("Timeout = %v, want %v", client.Timeout, original.Timeout)
	}
	if client.CheckRedirect == nil {
		t.Fatal("redirect policy was not set")
	}
	if err := client.CheckRedirect(nil, nil); err != http.ErrUseLastResponse {
		t.Fatalf("redirect error = %v, want http.ErrUseLastResponse", err)
	}
}

func TestHTTPClientForStreamingDisablesTotalTimeout(t *testing.T) {
	original := &http.Client{Timeout: 3 * time.Second}
	client := HTTPClientForStreaming(original)
	if client.Timeout != 0 {
		t.Fatalf("Timeout = %v, want 0", client.Timeout)
	}
	if original.Timeout != 3*time.Second {
		t.Fatalf("original Timeout = %v", original.Timeout)
	}
}
