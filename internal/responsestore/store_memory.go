package responsestore

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

const (
	// DefaultMemoryStoreTTL bounds in-memory response retention by age.
	DefaultMemoryStoreTTL = 24 * time.Hour
	// DefaultMemoryStoreMaxEntries bounds in-memory response retention by count.
	DefaultMemoryStoreMaxEntries = 10000
	// DefaultMemoryStoreMaxBytes bounds in-memory response retention by total
	// serialized size. Agentic clients resend whole conversations as input, so
	// entry counts alone do not bound memory.
	DefaultMemoryStoreMaxBytes = 64 << 20
	// DefaultMemoryStoreCleanupInterval limits full expired-entry sweeps.
	DefaultMemoryStoreCleanupInterval = time.Minute
)

// MemoryStore keeps response snapshots in process memory.
// Data survives across requests but not process restarts.
type MemoryStore struct {
	mu              sync.RWMutex
	items           map[string]*StoredResponse
	sizes           map[string]int64
	totalBytes      int64
	ttl             time.Duration
	maxEntries      int
	maxBytes        int64
	lastCleanup     time.Time
	cleanupInterval time.Duration
}

// MemoryStoreOption configures bounded in-memory response retention.
type MemoryStoreOption func(*MemoryStore)

// WithTTL expires stored responses after ttl. Non-positive values disable TTL.
func WithTTL(ttl time.Duration) MemoryStoreOption {
	return func(s *MemoryStore) {
		s.ttl = ttl
	}
}

// WithMaxEntries caps stored responses with FIFO eviction. Non-positive values disable the cap.
func WithMaxEntries(maxEntries int) MemoryStoreOption {
	return func(s *MemoryStore) {
		s.maxEntries = maxEntries
	}
}

// WithMaxBytes caps the total serialized size of stored responses with FIFO
// eviction. Non-positive values disable the cap.
func WithMaxBytes(maxBytes int64) MemoryStoreOption {
	return func(s *MemoryStore) {
		s.maxBytes = maxBytes
	}
}

// WithUnboundedRetention disables default in-memory retention bounds.
func WithUnboundedRetention() MemoryStoreOption {
	return func(s *MemoryStore) {
		s.ttl = 0
		s.maxEntries = 0
		s.maxBytes = 0
	}
}

// NewMemoryStore creates an empty in-memory response store.
// By default retention is bounded; pass WithUnboundedRetention to opt out.
func NewMemoryStore(options ...MemoryStoreOption) *MemoryStore {
	store := &MemoryStore{
		items:           make(map[string]*StoredResponse),
		sizes:           make(map[string]int64),
		ttl:             DefaultMemoryStoreTTL,
		maxEntries:      DefaultMemoryStoreMaxEntries,
		maxBytes:        DefaultMemoryStoreMaxBytes,
		cleanupInterval: DefaultMemoryStoreCleanupInterval,
	}
	for _, option := range options {
		if option != nil {
			option(store)
		}
	}
	return store
}

// Create stores a new response snapshot.
func (s *MemoryStore) Create(_ context.Context, response *StoredResponse) error {
	if response == nil || response.Response == nil || response.Response.ID == "" {
		return fmt.Errorf("response id is required")
	}

	c, size, err := cloneResponseWithSize(response)
	if err != nil {
		return err
	}
	if err := s.checkByteBudget(size); err != nil {
		return err
	}

	now := time.Now().UTC()
	prepareStoredResponseForMemory(c, now, s.ttl)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(now)
	if responseExpired(c, now) {
		return nil
	}
	if existing, exists := s.items[c.Response.ID]; exists {
		if !responseExpired(existing, now) {
			return fmt.Errorf("response already exists: %s", c.Response.ID)
		}
		s.removeLocked(c.Response.ID)
	}
	s.putLocked(c.Response.ID, c, size)
	s.enforceBoundsLocked(c.Response.ID)
	return nil
}

// Get retrieves one response snapshot by id.
func (s *MemoryStore) Get(_ context.Context, id string) (*StoredResponse, error) {
	now := time.Now().UTC()
	s.mu.Lock()
	s.cleanupExpiredLocked(now)
	response, ok := s.items[id]
	if !ok {
		s.mu.Unlock()
		return nil, ErrNotFound
	}
	if responseExpired(response, now) {
		s.removeLocked(id)
		s.mu.Unlock()
		return nil, ErrNotFound
	}
	s.mu.Unlock()
	return cloneResponse(response)
}

// Update replaces an existing response snapshot.
func (s *MemoryStore) Update(_ context.Context, response *StoredResponse) error {
	if response == nil || response.Response == nil || response.Response.ID == "" {
		return fmt.Errorf("response id is required")
	}
	c, size, err := cloneResponseWithSize(response)
	if err != nil {
		return err
	}
	if err := s.checkByteBudget(size); err != nil {
		return err
	}

	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(now)
	existing, exists := s.items[c.Response.ID]
	if !exists {
		return ErrNotFound
	}
	if responseExpired(existing, now) {
		s.removeLocked(c.Response.ID)
		return ErrNotFound
	}
	if c.StoredAt.IsZero() {
		c.StoredAt = existing.StoredAt
	}
	if c.ExpiresAt.IsZero() {
		c.ExpiresAt = existing.ExpiresAt
	}
	prepareStoredResponseForMemory(c, now, s.ttl)
	if responseExpired(c, now) {
		s.removeLocked(c.Response.ID)
		return ErrNotFound
	}
	s.putLocked(c.Response.ID, c, size)
	s.enforceBoundsLocked(c.Response.ID)
	return nil
}

// Delete removes one response snapshot by id.
func (s *MemoryStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(time.Now().UTC())
	if _, exists := s.items[id]; !exists {
		return ErrNotFound
	}
	s.removeLocked(id)
	return nil
}

// Close releases resources (no-op for memory store).
func (s *MemoryStore) Close() error {
	return nil
}

// checkByteBudget rejects snapshots that could never fit the byte budget, so
// callers see an explicit storage failure instead of silent eviction churn.
func (s *MemoryStore) checkByteBudget(size int64) error {
	if s.maxBytes > 0 && size > s.maxBytes {
		return fmt.Errorf("response snapshot of %d bytes exceeds the in-memory store budget of %d bytes", size, s.maxBytes)
	}
	return nil
}

func (s *MemoryStore) putLocked(id string, response *StoredResponse, size int64) {
	s.removeLocked(id)
	s.items[id] = response
	s.sizes[id] = size
	s.totalBytes += size
}

func (s *MemoryStore) removeLocked(id string) {
	if _, ok := s.items[id]; !ok {
		return
	}
	delete(s.items, id)
	s.totalBytes -= s.sizes[id]
	delete(s.sizes, id)
}

func prepareStoredResponseForMemory(response *StoredResponse, now time.Time, ttl time.Duration) {
	if response.StoredAt.IsZero() {
		response.StoredAt = now
	}
	if ttl > 0 && response.ExpiresAt.IsZero() {
		response.ExpiresAt = response.StoredAt.Add(ttl)
	}
}

func (s *MemoryStore) cleanupExpiredLocked(now time.Time) {
	if s.ttl <= 0 {
		return
	}
	if s.cleanupInterval > 0 && !s.lastCleanup.IsZero() && now.Sub(s.lastCleanup) < s.cleanupInterval {
		return
	}
	s.lastCleanup = now
	for id, response := range s.items {
		if responseExpired(response, now) {
			s.removeLocked(id)
		}
	}
}

// enforceBoundsLocked evicts oldest-first until both the entry-count and
// byte-budget caps hold. The protect id — the entry the caller just wrote,
// which checkByteBudget guarantees fits on its own — is never evicted, so a
// successful write cannot be silently undone by its own bound enforcement.
func (s *MemoryStore) enforceBoundsLocked(protect string) {
	overEntries := s.maxEntries > 0 && len(s.items) > s.maxEntries
	overBytes := s.maxBytes > 0 && s.totalBytes > s.maxBytes
	if !overEntries && !overBytes {
		return
	}

	entries := make([]memoryStoreEntry, 0, len(s.items))
	for id, response := range s.items {
		entries = append(entries, memoryStoreEntry{
			id:       id,
			storedAt: responseStoredAt(response),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].storedAt.Equal(entries[j].storedAt) {
			return entries[i].id < entries[j].id
		}
		return entries[i].storedAt.Before(entries[j].storedAt)
	})
	for _, entry := range entries {
		if (s.maxEntries <= 0 || len(s.items) <= s.maxEntries) &&
			(s.maxBytes <= 0 || s.totalBytes <= s.maxBytes) {
			return
		}
		if entry.id == protect {
			continue
		}
		s.removeLocked(entry.id)
	}
}

type memoryStoreEntry struct {
	id       string
	storedAt time.Time
}

func responseExpired(response *StoredResponse, now time.Time) bool {
	return response != nil && !response.ExpiresAt.IsZero() && !response.ExpiresAt.After(now)
}

func responseStoredAt(response *StoredResponse) time.Time {
	if response == nil || response.StoredAt.IsZero() {
		return time.Time{}
	}
	return response.StoredAt
}
