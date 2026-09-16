package realtime

import (
	"strings"
	"sync"
	"time"

	"github.com/enterpilot/gomodel/internal/expiry"
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
	// expiries orders call ids by expiry, so making room costs a heap pop
	// instead of a scan of every registered call.
	expiries expiry.Index[string]
	ttl      time.Duration
	capacity int
	now      func() time.Time
}

// NewCallRegistry returns an empty registry with production defaults.
func NewCallRegistry() *CallRegistry {
	return &CallRegistry{
		entries:  make(map[string]callEntry),
		ttl:      DefaultCallTTL,
		capacity: maxCalls,
		now:      time.Now,
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
	_, exists := r.entries[callID]
	if !exists && len(r.entries) >= r.capacity {
		r.evictSoonestLocked()
	}
	expires := now.Add(r.ttl)
	r.entries[callID] = callEntry{route: route, expires: expires}
	if !exists {
		// A re-registration extends the entry's life without telling the
		// index; the sweeps notice when the id surfaces.
		r.expiries.Track(callID, expires)
	}
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

// pruneLocked drops expired entries, touching only the ids that are actually
// due rather than sweeping the whole registry on every registration.
func (r *CallRegistry) pruneLocked(now time.Time) {
	for {
		id, tracked, ok := r.expiries.Expired(now)
		if !ok {
			break
		}
		entry, live := r.entries[id]
		switch {
		case !live:
			// Already dropped, by Lookup or an earlier eviction.
		case entry.expires.After(tracked):
			// Re-registered since: it lives longer than the index recorded.
			r.expiries.Track(id, entry.expires)
		default:
			delete(r.entries, id)
		}
	}
	if r.expiries.Stale(len(r.entries)) {
		r.retrackLocked()
	}
}

// evictSoonestLocked removes the entry closest to expiry to make room.
func (r *CallRegistry) evictSoonestLocked() {
	for {
		id, tracked, ok := r.expiries.Soonest()
		if !ok {
			return
		}
		entry, live := r.entries[id]
		switch {
		case !live:
			continue
		case entry.expires.After(tracked):
			r.expiries.Track(id, entry.expires)
		default:
			delete(r.entries, id)
			return
		}
	}
}

// retrackLocked rebuilds the index from the live entries, dropping the ids
// Lookup removed without the index ever seeing them.
func (r *CallRegistry) retrackLocked() {
	r.expiries.Reset()
	for id, entry := range r.entries {
		r.expiries.Track(id, entry.expires)
	}
}
