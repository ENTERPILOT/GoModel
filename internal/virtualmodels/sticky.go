package virtualmodels

import (
	"sync"
	"time"
)

const (
	// stickySessionTTL bounds how long an idle session keeps its pinned target.
	stickySessionTTL = 6 * time.Hour
	// maxStickySessions caps the pin map; at capacity the entry expiring
	// soonest is evicted.
	maxStickySessions = 10000
)

type stickyKey struct {
	source  string
	session string
}

type stickyPin struct {
	qualified string
	expires   time.Time
}

// stickySessions remembers which target served a session per redirect source,
// so session-affine load balancing routes a conversation consistently. Like
// the round-robin counters it is per-instance state: after a restart (or on
// another replica) the first request of a session simply re-pins.
type stickySessions struct {
	mu      sync.Mutex
	entries map[stickyKey]stickyPin
	now     func() time.Time // injectable for tests; nil means time.Now
}

func (s *stickySessions) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// lookup returns and refreshes a viable existing pin. It lets callers avoid
// running their selection strategy for requests that are already pinned.
func (s *stickySessions) lookup(source, session string, viable func(string) bool) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := stickyKey{source: source, session: session}
	now := s.clock()
	existing, ok := s.entries[key]
	if !ok {
		return "", false
	}
	if !existing.expires.After(now) {
		delete(s.entries, key)
		return "", false
	}
	if !viable(existing.qualified) {
		return "", false
	}
	existing.expires = now.Add(stickySessionTTL)
	s.entries[key] = existing
	return existing.qualified, true
}

// resolve returns the target serving a session: the existing pin when it is
// still viable (refreshing its TTL), otherwise candidate, which it pins. It
// rechecks after strategy selection so concurrent first requests agree on the
// first pinned target.
func (s *stickySessions) resolve(source, session string, viable func(string) bool, candidate string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := stickyKey{source: source, session: session}
	now := s.clock()
	if existing, ok := s.entries[key]; ok {
		if existing.expires.After(now) && viable(existing.qualified) {
			existing.expires = now.Add(stickySessionTTL)
			s.entries[key] = existing
			return existing.qualified
		}
		// Expired, or the pinned target is gone/saturated: re-pin the candidate.
		delete(s.entries, key)
	}
	if candidate != "" {
		if s.entries == nil {
			s.entries = make(map[stickyKey]stickyPin)
		}
		s.pruneLocked(now)
		if len(s.entries) >= maxStickySessions {
			s.evictSoonestLocked()
		}
		s.entries[key] = stickyPin{
			qualified: candidate,
			expires:   now.Add(stickySessionTTL),
		}
	}
	return candidate
}

// prune drops expired pins and pins for redirect sources no longer present in
// the latest snapshot, mirroring roundRobin.prune.
func (s *stickySessions) prune(active map[string]*redirectEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	for key, pin := range s.entries {
		if !pin.expires.After(now) {
			delete(s.entries, key)
			continue
		}
		if _, exists := active[key.source]; !exists {
			delete(s.entries, key)
		}
	}
}

func (s *stickySessions) pruneLocked(now time.Time) {
	for key, pin := range s.entries {
		if !pin.expires.After(now) {
			delete(s.entries, key)
		}
	}
}

func (s *stickySessions) evictSoonestLocked() {
	var soonestKey stickyKey
	var soonest time.Time
	first := true
	for key, pin := range s.entries {
		if first || pin.expires.Before(soonest) {
			soonestKey, soonest, first = key, pin.expires, false
		}
	}
	if !first {
		delete(s.entries, soonestKey)
	}
}
