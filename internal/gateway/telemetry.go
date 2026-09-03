package gateway

import (
	"sync"
	"time"
)

const defaultTelemetrySampleCapacity = 64

type ProviderTelemetrySnapshot struct {
	Provider                     string
	Attempts                     uint64
	Successes                    uint64
	Failures                     uint64
	Cancellations                uint64
	Timeouts                     uint64
	UnavailableFailures          uint64
	RateLimitFailures            uint64
	InvalidRequestFailures       uint64
	InternalFailures             uint64
	OtherFailures                uint64
	LastAttempt                  time.Time
	LastSuccess                  time.Time
	LastFailure                  time.Time
	NonStreamingLatencies        []time.Duration
	StreamingTimeToFirstContent  []time.Duration
	StreamingDurations           []time.Duration
	NonStreamingLatencySamples   []LatencySample
	StreamingFirstContentSamples []LatencySample
}

type LatencySample struct {
	Duration   time.Duration
	ObservedAt time.Time
}

type telemetryTracker struct {
	providers map[string]*providerTelemetry
	now       func() time.Time
}

type providerTelemetry struct {
	mu sync.Mutex

	attempts               uint64
	successes              uint64
	failures               uint64
	cancellations          uint64
	timeouts               uint64
	unavailableFailures    uint64
	rateLimitFailures      uint64
	invalidRequestFailures uint64
	internalFailures       uint64
	otherFailures          uint64
	lastAttempt            time.Time
	lastSuccess            time.Time
	lastFailure            time.Time
	nonStreamingLatencies  latencyRing
	streamingTTFC          latencyRing
	streamingDurations     durationRing
}

type telemetryAttempt struct {
	tracker              *telemetryTracker
	provider             *providerTelemetry
	started              time.Time
	firstContentRecorded bool
}

func newTelemetryTracker(providerNames []string, sampleCapacity int, now func() time.Time) *telemetryTracker {
	if now == nil {
		now = time.Now
	}
	tracker := &telemetryTracker{
		providers: make(map[string]*providerTelemetry, len(providerNames)),
		now:       now,
	}
	for _, name := range providerNames {
		tracker.providers[name] = &providerTelemetry{
			nonStreamingLatencies: newLatencyRing(sampleCapacity),
			streamingTTFC:         newLatencyRing(sampleCapacity),
			streamingDurations:    newDurationRing(sampleCapacity),
		}
	}
	return tracker
}

func (t *telemetryTracker) begin(providerName string) *telemetryAttempt {
	providerTelemetry := t.providers[providerName]
	if providerTelemetry == nil {
		return nil
	}
	now := t.now()
	providerTelemetry.mu.Lock()
	providerTelemetry.attempts++
	providerTelemetry.lastAttempt = now
	providerTelemetry.mu.Unlock()
	return &telemetryAttempt{tracker: t, provider: providerTelemetry, started: now}
}

func (a *telemetryAttempt) firstContent() (time.Duration, bool) {
	if a == nil || a.firstContentRecorded {
		return 0, false
	}
	a.firstContentRecorded = true
	now := a.tracker.now()
	duration := now.Sub(a.started)
	a.provider.mu.Lock()
	a.provider.streamingTTFC.add(duration, now)
	a.provider.mu.Unlock()
	return duration, true
}

func (a *telemetryAttempt) finishNonStreaming(outcome providerOutcome) time.Duration {
	if a == nil {
		return 0
	}
	now := a.tracker.now()
	duration := now.Sub(a.started)
	a.provider.mu.Lock()
	a.provider.recordOutcome(outcome, now)
	a.provider.nonStreamingLatencies.add(duration, now)
	a.provider.mu.Unlock()
	return duration
}

func (a *telemetryAttempt) finishStreaming(outcome providerOutcome) time.Duration {
	if a == nil {
		return 0
	}
	now := a.tracker.now()
	duration := now.Sub(a.started)
	a.provider.mu.Lock()
	a.provider.recordOutcome(outcome, now)
	a.provider.streamingDurations.add(duration)
	a.provider.mu.Unlock()
	return duration
}

func (p *providerTelemetry) recordOutcome(outcome providerOutcome, now time.Time) {
	switch outcome {
	case outcomeSuccess:
		p.successes++
		p.lastSuccess = now
	case outcomeCanceled:
		p.cancellations++
	default:
		p.failures++
		p.lastFailure = now
		switch outcome {
		case outcomeTimeout:
			p.timeouts++
		case outcomeUnavailable:
			p.unavailableFailures++
		case outcomeRateLimited:
			p.rateLimitFailures++
		case outcomeInvalidRequest:
			p.invalidRequestFailures++
		case outcomeInternal:
			p.internalFailures++
		default:
			p.otherFailures++
		}
	}
}

func (t *telemetryTracker) snapshot(providerName string) (ProviderTelemetrySnapshot, bool) {
	providerTelemetry := t.providers[providerName]
	if providerTelemetry == nil {
		return ProviderTelemetrySnapshot{}, false
	}
	providerTelemetry.mu.Lock()
	defer providerTelemetry.mu.Unlock()
	return ProviderTelemetrySnapshot{
		Provider:                     providerName,
		Attempts:                     providerTelemetry.attempts,
		Successes:                    providerTelemetry.successes,
		Failures:                     providerTelemetry.failures,
		Cancellations:                providerTelemetry.cancellations,
		Timeouts:                     providerTelemetry.timeouts,
		UnavailableFailures:          providerTelemetry.unavailableFailures,
		RateLimitFailures:            providerTelemetry.rateLimitFailures,
		InvalidRequestFailures:       providerTelemetry.invalidRequestFailures,
		InternalFailures:             providerTelemetry.internalFailures,
		OtherFailures:                providerTelemetry.otherFailures,
		LastAttempt:                  providerTelemetry.lastAttempt,
		LastSuccess:                  providerTelemetry.lastSuccess,
		LastFailure:                  providerTelemetry.lastFailure,
		NonStreamingLatencies:        providerTelemetry.nonStreamingLatencies.durations(),
		StreamingTimeToFirstContent:  providerTelemetry.streamingTTFC.durations(),
		StreamingDurations:           providerTelemetry.streamingDurations.snapshot(),
		NonStreamingLatencySamples:   providerTelemetry.nonStreamingLatencies.snapshot(),
		StreamingFirstContentSamples: providerTelemetry.streamingTTFC.snapshot(),
	}, true
}

type latencyRing struct {
	values []LatencySample
	next   int
	count  int
}

func newLatencyRing(capacity int) latencyRing {
	return latencyRing{values: make([]LatencySample, capacity)}
}

func (r *latencyRing) add(duration time.Duration, observedAt time.Time) {
	if len(r.values) == 0 {
		return
	}
	r.values[r.next] = LatencySample{Duration: duration, ObservedAt: observedAt}
	r.next = (r.next + 1) % len(r.values)
	if r.count < len(r.values) {
		r.count++
	}
}

func (r *latencyRing) snapshot() []LatencySample {
	values := make([]LatencySample, r.count)
	start := 0
	if r.count == len(r.values) {
		start = r.next
	}
	for i := range r.count {
		values[i] = r.values[(start+i)%len(r.values)]
	}
	return values
}

func (r *latencyRing) durations() []time.Duration {
	samples := r.snapshot()
	values := make([]time.Duration, len(samples))
	for i, sample := range samples {
		values[i] = sample.Duration
	}
	return values
}

type durationRing struct {
	values []time.Duration
	next   int
	count  int
}

func newDurationRing(capacity int) durationRing {
	return durationRing{values: make([]time.Duration, capacity)}
}

func (r *durationRing) add(value time.Duration) {
	if len(r.values) == 0 {
		return
	}
	r.values[r.next] = value
	r.next = (r.next + 1) % len(r.values)
	if r.count < len(r.values) {
		r.count++
	}
}

func (r *durationRing) snapshot() []time.Duration {
	values := make([]time.Duration, r.count)
	start := 0
	if r.count == len(r.values) {
		start = r.next
	}
	for i := range r.count {
		values[i] = r.values[(start+i)%len(r.values)]
	}
	return values
}
