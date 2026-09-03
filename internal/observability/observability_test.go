package observability

import (
	"context"
	"testing"
)

func TestDisabledSetupUsesNoopTracing(t *testing.T) {
	setup, err := New(context.Background(), false, "")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, span := setup.Tracer().Start(context.Background(), "test")
	if span.SpanContext().IsValid() {
		t.Fatal("disabled tracing produced a recording span")
	}
	span.End()
	if err := setup.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if got := setup.Propagator().Fields(); len(got) != 2 || got[0] != "traceparent" || got[1] != "tracestate" {
		t.Fatalf("propagator fields = %v", got)
	}
}

func TestTraceEndpointAddsDefaultTracePath(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "https://collector.example", want: "https://collector.example/v1/traces"},
		{input: "http://127.0.0.1:4318/", want: "http://127.0.0.1:4318/v1/traces"},
		{input: "https://collector.example/custom", want: "https://collector.example/custom"},
	} {
		if got := traceEndpoint(test.input); got != test.want {
			t.Fatalf("traceEndpoint(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}
