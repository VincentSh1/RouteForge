package gateway

import (
	"sync"
	"time"
)

const defaultTelemetrySampleCapacity = 64

type ProviderTelemetrySnapshot struct {
	Provider                    string
	Attempts                    uint64
	Successes                   uint64
	Failures                    uint64
	Cancellations               uint64
	Timeouts                    uint64
	UnavailableFailures         uint64
	RateLimitFailures           uint64
	InvalidRequestFailures      uint64
	InternalFailures            uint64
	OtherFailures               uint64
	LastAttempt                 time.Time
	LastSuccess                 time.Time
	LastFailure                 time.Time
	NonStreamingLatencies       []time.Duration
	StreamingTimeToFirstContent []time.Duration
	StreamingDurations          []time.Duration
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
	nonStreamingLatencies  durationRing
	streamingTTFC          durationRing
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
			nonStreamingLatencies: newDurationRing(sampleCapacity),
			streamingTTFC:         newDurationRing(sampleCapacity),
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

func (a *telemetryAttempt) firstContent() {
	if a == nil || a.firstContentRecorded {
		return
	}
	a.firstContentRecorded = true
	duration := a.tracker.now().Sub(a.started)
	a.provider.mu.Lock()
	a.provider.streamingTTFC.add(duration)
	a.provider.mu.Unlock()
}

func (a *telemetryAttempt) finishNonStreaming(outcome providerOutcome) {
	if a == nil {
		return
	}
	now := a.tracker.now()
	a.provider.mu.Lock()
	a.provider.recordOutcome(outcome, now)
	a.provider.nonStreamingLatencies.add(now.Sub(a.started))
	a.provider.mu.Unlock()
}

func (a *telemetryAttempt) finishStreaming(outcome providerOutcome) {
	if a == nil {
		return
	}
	now := a.tracker.now()
	a.provider.mu.Lock()
	a.provider.recordOutcome(outcome, now)
	a.provider.streamingDurations.add(now.Sub(a.started))
	a.provider.mu.Unlock()
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
		Provider:                    providerName,
		Attempts:                    providerTelemetry.attempts,
		Successes:                   providerTelemetry.successes,
		Failures:                    providerTelemetry.failures,
		Cancellations:               providerTelemetry.cancellations,
		Timeouts:                    providerTelemetry.timeouts,
		UnavailableFailures:         providerTelemetry.unavailableFailures,
		RateLimitFailures:           providerTelemetry.rateLimitFailures,
		InvalidRequestFailures:      providerTelemetry.invalidRequestFailures,
		InternalFailures:            providerTelemetry.internalFailures,
		OtherFailures:               providerTelemetry.otherFailures,
		LastAttempt:                 providerTelemetry.lastAttempt,
		LastSuccess:                 providerTelemetry.lastSuccess,
		LastFailure:                 providerTelemetry.lastFailure,
		NonStreamingLatencies:       providerTelemetry.nonStreamingLatencies.snapshot(),
		StreamingTimeToFirstContent: providerTelemetry.streamingTTFC.snapshot(),
		StreamingDurations:          providerTelemetry.streamingDurations.snapshot(),
	}, true
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
