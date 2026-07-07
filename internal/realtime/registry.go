package realtime

import (
	"strings"
	"sync"
	"time"
)

// DefaultCallTTL bounds how long a WebRTC call stays routable (registry entries
// and sideband observers): a realtime call outliving it has long ended upstream.
// maxCalls bounds memory if entries are registered faster than they expire.
// Both are far above realistic session counts and durations.
const (
	DefaultCallTTL = 6 * time.Hour
	maxCalls       = 10000
)

// CallRoute remembers which model and provider a WebRTC call was created with,
// so a later sideband attach (GET /v1/realtime?call_id=...) can route to the
// same upstream without the client restating them.
type CallRoute struct {
	Model    string
	Provider string
}

type callEntry struct {
	route   CallRoute
	expires time.Time
}

// CallRegistry is an in-memory call_id -> route map. Like the rate limit
// counters, it is per-instance state: after a restart (or on another replica)
// clients fall back to passing model and provider explicitly.
type CallRegistry struct {
	mu      sync.Mutex
	entries map[string]callEntry
	ttl     time.Duration
	now     func() time.Time
}

// NewCallRegistry returns an empty registry with production defaults.
func NewCallRegistry() *CallRegistry {
	return &CallRegistry{
		entries: make(map[string]callEntry),
		ttl:     DefaultCallTTL,
		now:     time.Now,
	}
}

// Register remembers the route for a call id. Empty ids are ignored.
func (r *CallRegistry) Register(callID string, route CallRoute) {
	callID = strings.TrimSpace(callID)
	if r == nil || callID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	r.pruneLocked(now)
	// Re-registering an existing id overwrites in place; only a genuinely new
	// entry at capacity needs to make room.
	if _, exists := r.entries[callID]; !exists && len(r.entries) >= maxCalls {
		r.evictSoonestLocked()
	}
	r.entries[callID] = callEntry{route: route, expires: now.Add(r.ttl)}
}

// Lookup returns the route registered for a call id, if it is still live.
func (r *CallRegistry) Lookup(callID string) (CallRoute, bool) {
	callID = strings.TrimSpace(callID)
	if r == nil || callID == "" {
		return CallRoute{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[callID]
	if !ok || r.now().After(entry.expires) {
		delete(r.entries, callID)
		return CallRoute{}, false
	}
	return entry.route, true
}

// pruneLocked drops expired entries. Registration happens at human call rates,
// so a full sweep per insert is cheap at the registry's bounded size.
func (r *CallRegistry) pruneLocked(now time.Time) {
	for id, entry := range r.entries {
		if now.After(entry.expires) {
			delete(r.entries, id)
		}
	}
}

// evictSoonestLocked removes the entry closest to expiry to make room.
func (r *CallRegistry) evictSoonestLocked() {
	var (
		victim  string
		soonest time.Time
	)
	for id, entry := range r.entries {
		if victim == "" || entry.expires.Before(soonest) {
			victim, soonest = id, entry.expires
		}
	}
	if victim != "" {
		delete(r.entries, victim)
	}
}
