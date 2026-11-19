package cache

import (
	"container/list"
	"context"
	"sync"
	"time"
)

// MemoryStore is a bounded in-memory Store for caches that must work without
// Redis. Entries expire after their TTL, and the oldest entries are evicted
// once keys and values together exceed the byte budget. It is local to the
// process: replicas each keep their own entries.
type MemoryStore struct {
	maxBytes int
	now      func() time.Time

	mu      sync.Mutex
	bytes   int
	order   *list.List // of *memoryEntry, oldest first
	entries map[string]*list.Element
}

type memoryEntry struct {
	key     string
	value   []byte
	expires time.Time
}

func (e *memoryEntry) size() int { return len(e.key) + len(e.value) }

// NewMemoryStore creates an in-memory store holding at most maxBytes.
func NewMemoryStore(maxBytes int) *MemoryStore {
	return &MemoryStore{
		maxBytes: maxBytes,
		now:      time.Now,
		order:    list.New(),
		entries:  make(map[string]*list.Element),
	}
}

// Get retrieves value by key; an expired entry reads as missing.
func (s *MemoryStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	elem, ok := s.entries[key]
	if !ok {
		return nil, nil
	}
	entry := elem.Value.(*memoryEntry)
	if !entry.expires.IsZero() && !s.now().Before(entry.expires) {
		s.remove(elem)
		return nil, nil
	}
	return append([]byte(nil), entry.value...), nil
}

// Set stores value for ttl; a ttl of zero keeps it until it is evicted. A
// value that cannot fit the byte budget is not stored.
func (s *MemoryStore) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	entry := &memoryEntry{key: key, value: append([]byte(nil), value...)}
	if ttl > 0 {
		entry.expires = s.now().Add(ttl)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if elem, ok := s.entries[key]; ok {
		s.remove(elem)
	}
	if entry.size() > s.maxBytes {
		return nil
	}
	s.entries[key] = s.order.PushBack(entry)
	s.bytes += entry.size()
	for s.bytes > s.maxBytes {
		s.remove(s.order.Front())
	}
	return nil
}

// remove drops one entry. The caller must hold s.mu.
func (s *MemoryStore) remove(elem *list.Element) {
	entry := s.order.Remove(elem).(*memoryEntry)
	delete(s.entries, entry.key)
	s.bytes -= entry.size()
}

// Close is a no-op.
func (s *MemoryStore) Close() error {
	return nil
}
