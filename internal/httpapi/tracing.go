package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/VincentSh1/RouteForge/internal/model"
	"github.com/VincentSh1/RouteForge/internal/observability"
	"github.com/VincentSh1/RouteForge/internal/openai"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const chatCompletionsRoute = "/v1/chat/completions"

type requestMetricState struct {
	streaming bool
}

type requestMetricStateKey struct{}

func TraceRequests(next http.Handler, tracer trace.Tracer, propagator propagation.TextMapPropagator, routingPolicy string, metrics *observability.Metrics) http.Handler {
	if metrics == nil {
		metrics = observability.NoopMetrics()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != chatCompletionsRoute {
			next.ServeHTTP(w, r)
			return
		}

		started := time.Now()
		metricState := &requestMetricState{}
		ctx := context.WithValue(propagator.Extract(r.Context(), propagation.HeaderCarrier(r.Header)), requestMetricStateKey{}, metricState)
		ctx, span := tracer.Start(ctx, "routeforge.request",
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("http.request.method", r.Method),
				attribute.String("http.route", chatCompletionsRoute),
				attribute.String("routeforge.routing.policy", routingPolicy),
			),
		)
		defer span.End()

		response := &tracingResponseWriter{ResponseWriter: w}
		var responseWriter http.ResponseWriter = response
		if flusher, ok := w.(http.Flusher); ok {
			responseWriter = &tracingFlushingResponseWriter{tracingResponseWriter: response, flusher: flusher}
		}
		next.ServeHTTP(responseWriter, r.WithContext(ctx))
		status := response.status
		if status == 0 {
			status = http.StatusOK
		}
		outcome := requestOutcome(ctx, status)
		if response.outcome != "" {
			outcome = response.outcome
		}
		span.SetAttributes(
			attribute.Int("http.response.status_code", status),
			attribute.String("routeforge.response.outcome", outcome),
		)
		if status >= http.StatusInternalServerError || outcome == "failure" {
			span.SetStatus(codes.Error, "server_error")
		}
		metrics.RecordRequest(ctx, routingPolicy, metricState.streaming, outcome, time.Since(started))
	})
}

func recordChatRequest(ctx context.Context, request openai.ChatCompletionRequest) {
	if state, ok := ctx.Value(requestMetricStateKey{}).(*requestMetricState); ok {
		state.streaming = request.Stream
	}
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(attribute.Bool("routeforge.request.streaming", request.Stream))
	if request.Model == model.General {
		span.SetAttributes(attribute.String("routeforge.request.model_alias", request.Model))
	}
}

func requestOutcome(ctx context.Context, status int) string {
	if ctx.Err() != nil {
		return "cancellation"
	}
	if status >= http.StatusInternalServerError {
		return "server_error"
	}
	if status >= http.StatusBadRequest {
		return "client_error"
	}
	return "success"
}

type tracingResponseWriter struct {
	http.ResponseWriter
	status  int
	outcome string
}

func (w *tracingResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *tracingResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(data)
}

func (w *tracingResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *tracingResponseWriter) setTraceOutcome(outcome string) { w.outcome = outcome }

type tracingFlushingResponseWriter struct {
	*tracingResponseWriter
	flusher http.Flusher
}

func (w *tracingFlushingResponseWriter) Flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.flusher.Flush()
}

func recordCommittedStreamFailure(w http.ResponseWriter, ctx context.Context) {
	outcome := "failure"
	if ctx.Err() != nil {
		outcome = "cancellation"
	}
	if writer, ok := w.(interface{ setTraceOutcome(string) }); ok {
		writer.setTraceOutcome(outcome)
	}
}
