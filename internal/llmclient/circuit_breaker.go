package llmclient

import (
	"regexp"
	"sync"
	"time"
)

// TripRule opens the breaker instantly when an upstream error message matches
// Pattern, skipping the failure threshold. A zero TTL defers to the breaker's
// open-state timeout at trip time.
type TripRule struct {
	Pattern *regexp.Regexp
	TTL     time.Duration
}

// circuitBreaker implements a circuit breaker pattern with half-open state protection
type circuitBreaker struct {
	mu               sync.Mutex
	state            circuitState
	failures         int
	successes        int
	failureThreshold int
	successThreshold int
	timeout          time.Duration
	lastFailure      time.Time
	// quotaUntil keeps the breaker open for a quota trip window even after
	// lastFailure ages past timeout. Zero unless a quota trip happened.
	quotaUntil      time.Time
	halfOpenAllowed bool // Controls single-request probe in half-open state
}

type circuitState int

const (
	circuitClosed circuitState = iota
	circuitOpen
	circuitHalfOpen
)

func newCircuitBreaker(failureThreshold, successThreshold int, timeout time.Duration) *circuitBreaker {
	return &circuitBreaker{
		state:            circuitClosed,
		failureThreshold: failureThreshold,
		successThreshold: successThreshold,
		timeout:          timeout,
		halfOpenAllowed:  true,
	}
}

// acquire checks if a request should be allowed through the circuit breaker.
// The second return value reports whether the caller is the single half-open probe.
func (cb *circuitBreaker) acquire() (bool, bool) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case circuitClosed:
		return true, false
	case circuitOpen:
		// Check if timeout has passed and no quota trip window is active
		if time.Since(cb.lastFailure) > cb.timeout && time.Now().After(cb.quotaUntil) {
			cb.state = circuitHalfOpen
			cb.successes = 0
			cb.halfOpenAllowed = true // Allow the first probe request
		} else {
			return false, false
		}
		// Fall through to half-open handling
		fallthrough
	case circuitHalfOpen:
		// Only allow one request through at a time in half-open state
		// This prevents thundering herd when transitioning from open
		if cb.halfOpenAllowed {
			cb.halfOpenAllowed = false
			return true, true
		}
		return false, false
	}
	return true, false
}

// releaseProbe returns the half-open probe slot without recording an outcome.
// Called when the probe request never produced a provider verdict (local
// request-build error or caller-side cancellation). Without it the breaker
// would stay half-open with the slot consumed and reject every request until
// process restart, because the timeout-based transition only runs from the
// open state.
func (cb *circuitBreaker) releaseProbe() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.state == circuitHalfOpen {
		cb.halfOpenAllowed = true
	}
}

// RecordSuccess records a successful request
func (cb *circuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case circuitHalfOpen:
		cb.successes++
		cb.halfOpenAllowed = true // Allow next probe request
		if cb.successes >= cb.successThreshold {
			cb.state = circuitClosed
			cb.failures = 0
		}
	case circuitClosed:
		cb.failures = 0
	}
}

// RecordFailure records a failed request
func (cb *circuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failures++
	cb.lastFailure = time.Now()

	switch cb.state {
	case circuitClosed:
		if cb.failures >= cb.failureThreshold {
			cb.state = circuitOpen
		}
	case circuitHalfOpen:
		cb.state = circuitOpen
		cb.successes = 0
		cb.halfOpenAllowed = true // Reset for next timeout period
	}
}

// RecordQuotaTrip opens the breaker instantly, without counting failures,
// because a matching upstream error proves the provider's quota is gone. The
// breaker stays open for ttl regardless of the open-state timeout; acquire
// honors whichever deadline expires last.
func (cb *circuitBreaker) RecordQuotaTrip(ttl time.Duration) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := time.Now()
	cb.state = circuitOpen
	cb.lastFailure = now
	cb.quotaUntil = now.Add(ttl)
	cb.successes = 0
	cb.halfOpenAllowed = true // Reset for next timeout period
}

// Reset force-closes the breaker and clears any quota trip window.
func (cb *circuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.state = circuitClosed
	cb.failures = 0
	cb.successes = 0
	cb.quotaUntil = time.Time{}
	cb.halfOpenAllowed = true
}

// State returns the current circuit state (for testing/monitoring)
func (cb *circuitBreaker) State() string {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case circuitClosed:
		return "closed"
	case circuitOpen:
		return "open"
	case circuitHalfOpen:
		return "half-open"
	}
	return "unknown"
}
