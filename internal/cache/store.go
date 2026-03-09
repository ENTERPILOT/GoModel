// Package cache provides a generic key-value store abstraction.
// Concrete backends (Redis, in-memory) implement the Store interface.
// Domain-specific caches (model cache, response cache) build on top of Store.
package cache

import (
	"context"
	"sync"
	"time"
)

// Store is a generic key-value store. RedisStore and MapStore implement it.
type Store interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Close() error
}

// MapStore is an in-memory Store for testing.
type MapStore struct {
	mu   sync.RWMutex
	data map[string][]byte
}

// NewMapStore creates an in-memory store.
func NewMapStore() *MapStore {
	return &MapStore{data: make(map[string][]byte)}
}

// Get retrieves value by key.
func (s *MapStore) Get(ctx context.Context, key string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.data[key]
	if !ok {
		return nil, nil
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}

// Set stores value. TTL is ignored.
func (s *MapStore) Set(ctx context.Context, key string, value []byte, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = make(map[string][]byte)
	}
	cp := make([]byte, len(value))
	copy(cp, value)
	s.data[key] = cp
	return nil
}

// Close is a no-op.
func (s *MapStore) Close() error {
	return nil
}
