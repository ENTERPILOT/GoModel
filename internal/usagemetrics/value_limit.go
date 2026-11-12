package usagemetrics

import "sync"

// Overflow buckets for dimension values seen after the cap is reached, or
// that are too long or reserved.
const (
	OtherValue    = "{other}"
	OtherUserPath = "/" + OtherValue
)

// MaxValueBytes is the longest dimension value exported as itself. Every
// admitted value is copied into each of its series and into every scrape, so
// a longer one goes to the overflow bucket without taking a slot.
const MaxValueBytes = 256

// reserved reports whether value is one of the names GoModel reports on its
// own, with or without the user path's leading slash. A caller value spelled like one goes to the overflow bucket, so it can
// never pass for the overflow or unknown bucket, nor take a slot.
func reserved(value string) bool {
	switch value {
	case OtherValue, OtherUserPath, UnknownAuthKey, "/" + UnknownAuthKey:
		return true
	}
	return false
}

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
// value means "no value" and is never counted. A value longer than
// MaxValueBytes, or spelled like a reserved name, is overflow and takes no
// slot.
func (l *valueLimit) admit(value, overflow string, limit int) string {
	if value == "" {
		return ""
	}
	if len(value) > MaxValueBytes || reserved(value) {
		return overflow
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
