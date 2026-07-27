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

// lookup returns the pinned target for a session, refreshing its TTL. Expired
// pins are dropped on read.
func (s *stickySessions) lookup(source, session string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := stickyKey{source: source, session: session}
	pin, ok := s.entries[key]
	if !ok {
		return "", false
	}
	now := s.clock()
	if !pin.expires.After(now) {
		delete(s.entries, key)
		return "", false
	}
	pin.expires = now.Add(stickySessionTTL)
	s.entries[key] = pin
	return pin.qualified, true
}

// pin remembers the target chosen for a session.
func (s *stickySessions) pin(source, session, qualified string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	if s.entries == nil {
		s.entries = make(map[stickyKey]stickyPin)
	}
	s.pruneLocked(now)
	if len(s.entries) >= maxStickySessions {
		s.evictSoonestLocked()
	}
	s.entries[stickyKey{source: source, session: session}] = stickyPin{
		qualified: qualified,
		expires:   now.Add(stickySessionTTL),
	}
}

// prune drops expired pins and pins for redirect sources no longer present in
// the latest snapshot, mirroring roundRobin.prune.
func (s *stickySessions) prune(active map[string]redirectEntry) {
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
