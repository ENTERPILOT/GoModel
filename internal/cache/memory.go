package cache

import (
	"container/heap"
	"context"
	"sync"
	"time"
)

// memoryEntryOverhead approximates what one entry costs beyond its key and
// value (the entry itself, its map slot, and its queue slot), so a store full
// of small values still stays near its byte budget.
const memoryEntryOverhead = 128

// MemoryStore is a bounded in-memory Store for caches that must work without
// Redis. Entries expire after their TTL. Once the entries exceed the byte
// budget, the ones closest to expiry are evicted first, so an expired entry
// always goes before a live one. It is local to the process: replicas each
// keep their own entries.
type MemoryStore struct {
	maxBytes int
	now      func() time.Time

	mu      sync.Mutex
	bytes   int
	queue   expiryQueue
	entries map[string]*memoryEntry
}

type memoryEntry struct {
	key   string
	value []byte
	// expires is zero for an entry stored without a TTL.
	expires time.Time
	// index is the entry's position in the queue.
	index int
}

func (e *memoryEntry) size() int { return memoryEntryOverhead + len(e.key) + len(e.value) }

// expiryQueue is a heap with the entry that expires first on top; entries
// that never expire sort last.
type expiryQueue []*memoryEntry

func (q expiryQueue) Len() int { return len(q) }

func (q expiryQueue) Less(i, j int) bool {
	a, b := q[i].expires, q[j].expires
	if a.IsZero() || b.IsZero() {
		return !a.IsZero()
	}
	return a.Before(b)
}

func (q expiryQueue) Swap(i, j int) {
	q[i], q[j] = q[j], q[i]
	q[i].index = i
	q[j].index = j
}

func (q *expiryQueue) Push(x any) {
	entry := x.(*memoryEntry)
	entry.index = len(*q)
	*q = append(*q, entry)
}

func (q *expiryQueue) Pop() any {
	old := *q
	last := len(old) - 1
	entry := old[last]
	old[last] = nil
	*q = old[:last]
	return entry
}

// NewMemoryStore creates an in-memory store holding at most maxBytes.
func NewMemoryStore(maxBytes int) *MemoryStore {
	return &MemoryStore{
		maxBytes: maxBytes,
		now:      time.Now,
		entries:  make(map[string]*memoryEntry),
	}
}

// Get retrieves value by key; an expired entry reads as missing.
func (s *MemoryStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[key]
	if !ok {
		return nil, nil
	}
	if !entry.expires.IsZero() && !s.now().Before(entry.expires) {
		s.remove(entry)
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
	if previous, ok := s.entries[key]; ok {
		s.remove(previous)
	}
	if entry.size() > s.maxBytes {
		return nil
	}
	heap.Push(&s.queue, entry)
	s.entries[key] = entry
	s.bytes += entry.size()
	for s.bytes > s.maxBytes {
		s.remove(s.queue[0])
	}
	return nil
}

// remove drops one entry. The caller must hold s.mu.
func (s *MemoryStore) remove(entry *memoryEntry) {
	heap.Remove(&s.queue, entry.index)
	delete(s.entries, entry.key)
	s.bytes -= entry.size()
}

// Close is a no-op.
func (s *MemoryStore) Close() error {
	return nil
}
