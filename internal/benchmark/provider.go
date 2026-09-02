package benchmark

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/VincentSh1/RouteForge/internal/openai"
	providerpkg "github.com/VincentSh1/RouteForge/internal/provider"
)

type virtualClock struct {
	mu  sync.Mutex
	now time.Time
}

func newVirtualClock() *virtualClock {
	return &virtualClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *virtualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *virtualClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

type attemptRecord struct {
	Provider  string
	Model     string
	Condition ProviderCondition
}

type attemptRecorder struct {
	mu      sync.Mutex
	records []attemptRecord
}

func (r *attemptRecorder) add(record attemptRecord) {
	r.mu.Lock()
	r.records = append(r.records, record)
	r.mu.Unlock()
}

func (r *attemptRecorder) reset() {
	r.mu.Lock()
	r.records = nil
	r.mu.Unlock()
}

func (r *attemptRecorder) snapshot() []attemptRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]attemptRecord(nil), r.records...)
}

type simulatedProvider struct {
	name     string
	clock    *virtualClock
	recorder *attemptRecorder

	mu        sync.Mutex
	condition ProviderCondition
}

func (p *simulatedProvider) Name() string { return p.name }

func (p *simulatedProvider) prepare(condition ProviderCondition) {
	p.mu.Lock()
	p.condition = cloneCondition(condition)
	p.mu.Unlock()
}

func (p *simulatedProvider) current() ProviderCondition {
	p.mu.Lock()
	defer p.mu.Unlock()
	return cloneCondition(p.condition)
}

func (p *simulatedProvider) Complete(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	condition := p.current()
	p.recorder.add(attemptRecord{Provider: p.name, Model: request.Model, Condition: condition})
	p.clock.Advance(condition.CompletionLatency)
	response := openai.ChatCompletionResponse{
		ID: "benchmark-" + p.name, Object: "chat.completion", Model: request.Model,
		Usage: cloneUsage(condition.Usage),
	}
	return response, conditionError(ctx, p.name, condition)
}

func (p *simulatedProvider) Stream(ctx context.Context, request openai.ChatCompletionRequest) (providerpkg.Stream, error) {
	condition := p.current()
	p.recorder.add(attemptRecord{Provider: p.name, Model: request.Model, Condition: condition})
	return newSimulatedStream(ctx, p.name, request.Model, p.clock, condition), nil
}

type streamStep struct {
	delay time.Duration
	chunk providerpkg.StreamChunk
	err   error
}

type simulatedStream struct {
	ctx   context.Context
	clock *virtualClock
	steps []streamStep
	index int
}

func newSimulatedStream(ctx context.Context, providerName, modelName string, clock *virtualClock, condition ProviderCondition) *simulatedStream {
	err := conditionError(ctx, providerName, condition)
	usage := cloneUsage(condition.Usage)
	content := providerpkg.StreamChunk{ID: "benchmark-" + providerName, Model: modelName, Role: "assistant", Content: "x"}
	usageChunk := providerpkg.StreamChunk{ID: "benchmark-" + providerName, Model: modelName, Usage: usage}
	remaining := condition.StreamDuration - condition.TTFC

	var steps []streamStep
	switch condition.StreamFailure {
	case FailureBeforeCommit:
		if usage != nil {
			steps = append(steps, streamStep{delay: condition.StreamDuration, chunk: usageChunk})
			steps = append(steps, streamStep{err: err})
		} else {
			steps = append(steps, streamStep{delay: condition.StreamDuration, err: err})
		}
	case FailureAfterCommit:
		steps = append(steps, streamStep{delay: condition.TTFC, chunk: content})
		if usage != nil {
			steps = append(steps, streamStep{delay: remaining, chunk: usageChunk})
			steps = append(steps, streamStep{err: err})
		} else {
			steps = append(steps, streamStep{delay: remaining, err: err})
		}
	default:
		steps = append(steps, streamStep{delay: condition.TTFC, chunk: content})
		usageChunk.FinishReason = "stop"
		steps = append(steps, streamStep{delay: remaining, chunk: usageChunk})
		steps = append(steps, streamStep{err: io.EOF})
	}
	return &simulatedStream{ctx: ctx, clock: clock, steps: steps}
}

func (s *simulatedStream) Next() (providerpkg.StreamChunk, error) {
	if err := s.ctx.Err(); err != nil {
		return providerpkg.StreamChunk{}, err
	}
	if s.index >= len(s.steps) {
		return providerpkg.StreamChunk{}, io.EOF
	}
	step := s.steps[s.index]
	s.index++
	s.clock.Advance(step.delay)
	return step.chunk, step.err
}

func (*simulatedStream) Close() error { return nil }

func conditionError(ctx context.Context, providerName string, condition ProviderCondition) error {
	if condition.Canceled {
		return context.Canceled
	}
	if condition.ErrorKind == "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return providerpkg.NewError(condition.ErrorKind, providerName, errors.New("simulated provider failure"))
}

func cloneCondition(condition ProviderCondition) ProviderCondition {
	condition.Usage = cloneUsage(condition.Usage)
	return condition
}

func cloneUsage(usage *openai.Usage) *openai.Usage {
	if usage == nil {
		return nil
	}
	return &openai.Usage{
		InputTokens: cloneUint64(usage.InputTokens), OutputTokens: cloneUint64(usage.OutputTokens), TotalTokens: cloneUint64(usage.TotalTokens),
	}
}

func cloneUint64(value *uint64) *uint64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
