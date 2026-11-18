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
	expiry.Prune(&r.expiries, r.entries, callExpires, now)
	// Re-registering an existing id overwrites in place; only a genuinely new
	// entry at capacity needs to make room.
	_, exists := r.entries[callID]
	if !exists && len(r.entries) >= r.capacity {
		expiry.EvictSoonest(&r.expiries, r.entries, callExpires)
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
	if !ok || !entry.expires.After(r.now()) {
		delete(r.entries, callID)
		return CallRoute{}, false
	}
	return entry.route, true
}

func callExpires(entry callEntry) time.Time { return entry.expires }
