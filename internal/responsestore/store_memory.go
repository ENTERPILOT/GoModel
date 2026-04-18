package responsestore

import (
	"context"
	"fmt"
	"sync"
	"time"
)

const (
	// DefaultMemoryStoreTTL bounds in-memory response retention by age.
	DefaultMemoryStoreTTL = 24 * time.Hour
	// DefaultMemoryStoreMaxEntries bounds in-memory response retention by count.
	DefaultMemoryStoreMaxEntries = 10000
)

// MemoryStore keeps response snapshots in process memory.
// Data survives across requests but not process restarts.
type MemoryStore struct {
	mu         sync.RWMutex
	items      map[string]*StoredResponse
	ttl        time.Duration
	maxEntries int
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

// WithUnboundedRetention disables default in-memory retention bounds.
func WithUnboundedRetention() MemoryStoreOption {
	return func(s *MemoryStore) {
		s.ttl = 0
		s.maxEntries = 0
	}
}

// NewMemoryStore creates an empty in-memory response store.
// By default retention is bounded; pass WithUnboundedRetention to opt out.
func NewMemoryStore(options ...MemoryStoreOption) *MemoryStore {
	store := &MemoryStore{
		items:      make(map[string]*StoredResponse),
		ttl:        DefaultMemoryStoreTTL,
		maxEntries: DefaultMemoryStoreMaxEntries,
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

	c, err := cloneResponse(response)
	if err != nil {
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
	if _, exists := s.items[c.Response.ID]; exists {
		return fmt.Errorf("response already exists: %s", c.Response.ID)
	}
	s.items[c.Response.ID] = c
	s.enforceMaxEntriesLocked()
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
		delete(s.items, id)
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
	c, err := cloneResponse(response)
	if err != nil {
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
	if c.StoredAt.IsZero() {
		c.StoredAt = existing.StoredAt
	}
	if c.ExpiresAt.IsZero() {
		c.ExpiresAt = existing.ExpiresAt
	}
	prepareStoredResponseForMemory(c, now, s.ttl)
	if responseExpired(c, now) {
		delete(s.items, c.Response.ID)
		return ErrNotFound
	}
	s.items[c.Response.ID] = c
	s.enforceMaxEntriesLocked()
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
	delete(s.items, id)
	return nil
}

// Close releases resources (no-op for memory store).
func (s *MemoryStore) Close() error {
	return nil
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
	for id, response := range s.items {
		if responseExpired(response, now) {
			delete(s.items, id)
		}
	}
}

func (s *MemoryStore) enforceMaxEntriesLocked() {
	if s.maxEntries <= 0 {
		return
	}
	for len(s.items) > s.maxEntries {
		oldestID := ""
		var oldest time.Time
		for id, response := range s.items {
			storedAt := responseStoredAt(response)
			if oldestID == "" || storedAt.Before(oldest) {
				oldestID = id
				oldest = storedAt
			}
		}
		if oldestID == "" {
			return
		}
		delete(s.items, oldestID)
	}
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
