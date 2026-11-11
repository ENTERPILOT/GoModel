package usagemetrics

import "sync"

// Overflow buckets for dimension values seen after the cap is reached.
const (
	OtherValue    = "{other}"
	OtherUserPath = "/" + OtherValue
)

// admitted holds the caller values in use, per dimension. It lives for the
// whole process, not for one configuration: Prometheus series outlive a
// reload, so the cap on them has to as well.
var admitted struct {
	userPaths     valueLimit
	authKeys      valueLimit
	requestLabels valueLimit
}

// valueLimit bounds the distinct values of one metric dimension.
type valueLimit struct {
	mu   sync.RWMutex
	seen map[string]struct{}
}

// admit returns value while fewer than limit distinct values are in use, and
// overflow afterwards. An admitted value keeps its own series; the empty
// value means "no value" and is never counted.
func (l *valueLimit) admit(value, overflow string, limit int) string {
	if value == "" {
		return ""
	}
	l.mu.RLock()
	_, ok := l.seen[value]
	full := len(l.seen) >= limit
	l.mu.RUnlock()
	if ok {
		return value
	}
	if full {
		return overflow
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.seen[value]; ok {
		return value
	}
	if len(l.seen) >= limit {
		return overflow
	}
	if l.seen == nil {
		l.seen = make(map[string]struct{})
	}
	l.seen[value] = struct{}{}
	return value
}

func (l *valueLimit) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen = nil
}

// ResetCallerValues forgets every admitted caller value (useful for testing).
func ResetCallerValues() {
	admitted.userPaths.reset()
	admitted.authKeys.reset()
	admitted.requestLabels.reset()
}
