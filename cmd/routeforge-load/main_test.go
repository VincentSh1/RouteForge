package main

import (
	"context"
	"io"
	"testing"
)

func TestInvalidCLIFlagsDoNotSendRequests(t *testing.T) {
	for _, args := range [][]string{nil, {"-url", "http://example.invalid"}, {"-confirm-mock", "-concurrency", "1000"}, {"-confirm-mock", "-requests", "0"}, {"-confirm-mock", "-metrics-port", "-1"}} {
		if run(context.Background(), args, io.Discard) == nil {
			t.Fatal("unsafe CLI accepted")
		}
	}
}
