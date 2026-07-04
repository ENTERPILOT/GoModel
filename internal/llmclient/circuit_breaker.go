package llmclient

import (
	"sync"
	"time"
)

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
	halfOpenAllowed  bool // Controls single-request probe in half-open state
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
		// Check if timeout has passed
		if time.Since(cb.lastFailure) > cb.timeout {
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

func (cb *circuitBreaker) IsHalfOpen() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state == circuitHalfOpen
}
