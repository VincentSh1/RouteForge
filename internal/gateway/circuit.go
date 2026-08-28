package gateway

import (
	"sync"
	"time"
)

type circuitState string

const (
	circuitClosed   circuitState = "closed"
	circuitOpen     circuitState = "open"
	circuitHalfOpen circuitState = "half_open"
)

type CircuitConfig struct {
	FailureThreshold int
	OpenDuration     time.Duration
}

type healthTracker struct {
	providers        map[string]*providerHealth
	failureThreshold int
	openDuration     time.Duration
	now              func() time.Time
}

type providerHealth struct {
	mu                  sync.Mutex
	state               circuitState
	consecutiveFailures int
	lastSuccess         time.Time
	lastFailure         time.Time
	openUntil           time.Time
	halfOpenInFlight    bool
}

type healthAttempt struct {
	tracker  *healthTracker
	provider *providerHealth
	halfOpen bool
}

type healthSnapshot struct {
	State               circuitState
	ConsecutiveFailures int
	LastSuccess         time.Time
	LastFailure         time.Time
	OpenUntil           time.Time
	HalfOpenInFlight    bool
}

func newHealthTracker(providerNames []string, config CircuitConfig, now func() time.Time) *healthTracker {
	if now == nil {
		now = time.Now
	}
	tracker := &healthTracker{
		providers:        make(map[string]*providerHealth, len(providerNames)),
		failureThreshold: config.FailureThreshold,
		openDuration:     config.OpenDuration,
		now:              now,
	}
	for _, name := range providerNames {
		tracker.providers[name] = &providerHealth{state: circuitClosed}
	}
	return tracker
}

func (t *healthTracker) begin(providerName string) (*healthAttempt, bool) {
	health := t.providers[providerName]
	if health == nil {
		return nil, false
	}

	health.mu.Lock()
	defer health.mu.Unlock()

	switch health.state {
	case circuitOpen:
		if t.now().Before(health.openUntil) {
			return nil, false
		}
		health.state = circuitHalfOpen
		health.halfOpenInFlight = true
		return &healthAttempt{tracker: t, provider: health, halfOpen: true}, true
	case circuitHalfOpen:
		if health.halfOpenInFlight {
			return nil, false
		}
		health.halfOpenInFlight = true
		return &healthAttempt{tracker: t, provider: health, halfOpen: true}, true
	default:
		return &healthAttempt{tracker: t, provider: health}, true
	}
}

// eligible reports whether a request could currently be admitted without
// reserving a HALF_OPEN trial. begin remains the authoritative atomic check.
func (t *healthTracker) eligible(providerName string) bool {
	health := t.providers[providerName]
	if health == nil {
		return false
	}

	health.mu.Lock()
	defer health.mu.Unlock()

	switch health.state {
	case circuitOpen:
		return !t.now().Before(health.openUntil)
	case circuitHalfOpen:
		return !health.halfOpenInFlight
	default:
		return true
	}
}

func (a *healthAttempt) success() {
	health := a.provider
	health.mu.Lock()
	defer health.mu.Unlock()
	health.state = circuitClosed
	health.consecutiveFailures = 0
	health.lastSuccess = a.tracker.now()
	health.openUntil = time.Time{}
	health.halfOpenInFlight = false
}

func (a *healthAttempt) failure() {
	health := a.provider
	health.mu.Lock()
	defer health.mu.Unlock()
	now := a.tracker.now()
	health.lastFailure = now
	health.consecutiveFailures++
	if a.halfOpen || health.consecutiveFailures >= a.tracker.failureThreshold {
		health.state = circuitOpen
		health.openUntil = now.Add(a.tracker.openDuration)
		health.halfOpenInFlight = false
	}
}

func (a *healthAttempt) ignore() {
	if !a.halfOpen {
		return
	}
	health := a.provider
	health.mu.Lock()
	health.halfOpenInFlight = false
	health.mu.Unlock()
}

func (t *healthTracker) snapshot(providerName string) (healthSnapshot, bool) {
	health := t.providers[providerName]
	if health == nil {
		return healthSnapshot{}, false
	}
	health.mu.Lock()
	defer health.mu.Unlock()
	return healthSnapshot{
		State:               health.state,
		ConsecutiveFailures: health.consecutiveFailures,
		LastSuccess:         health.lastSuccess,
		LastFailure:         health.lastFailure,
		OpenUntil:           health.openUntil,
		HalfOpenInFlight:    health.halfOpenInFlight,
	}, true
}
