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
	observerMu       sync.RWMutex
	onTransition     func(string, circuitState, circuitState)
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
	name     string
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
		onTransition:     func(string, circuitState, circuitState) {},
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

	switch health.state {
	case circuitOpen:
		if t.now().Before(health.openUntil) {
			health.mu.Unlock()
			return nil, false
		}
		health.state = circuitHalfOpen
		health.halfOpenInFlight = true
		health.mu.Unlock()
		t.notifyTransition(providerName, circuitOpen, circuitHalfOpen)
		return &healthAttempt{tracker: t, provider: health, name: providerName, halfOpen: true}, true
	case circuitHalfOpen:
		if health.halfOpenInFlight {
			health.mu.Unlock()
			return nil, false
		}
		health.halfOpenInFlight = true
		health.mu.Unlock()
		return &healthAttempt{tracker: t, provider: health, name: providerName, halfOpen: true}, true
	default:
		health.mu.Unlock()
		return &healthAttempt{tracker: t, provider: health, name: providerName}, true
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
	previous := health.state
	health.state = circuitClosed
	health.consecutiveFailures = 0
	health.lastSuccess = a.tracker.now()
	health.openUntil = time.Time{}
	health.halfOpenInFlight = false
	health.mu.Unlock()
	if previous != circuitClosed {
		a.tracker.notifyTransition(a.name, previous, circuitClosed)
	}
}

func (a *healthAttempt) failure() {
	health := a.provider
	health.mu.Lock()
	now := a.tracker.now()
	previous := health.state
	health.lastFailure = now
	health.consecutiveFailures++
	if a.halfOpen || health.consecutiveFailures >= a.tracker.failureThreshold {
		health.state = circuitOpen
		health.openUntil = now.Add(a.tracker.openDuration)
		health.halfOpenInFlight = false
	}
	current := health.state
	health.mu.Unlock()
	if current != previous {
		a.tracker.notifyTransition(a.name, previous, current)
	}
}

func (t *healthTracker) setTransitionObserver(observer func(string, circuitState, circuitState)) {
	if observer == nil {
		observer = func(string, circuitState, circuitState) {}
	}
	t.observerMu.Lock()
	t.onTransition = observer
	t.observerMu.Unlock()
}

func (t *healthTracker) notifyTransition(providerName string, from, to circuitState) {
	t.observerMu.RLock()
	observer := t.onTransition
	t.observerMu.RUnlock()
	observer(providerName, from, to)
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
