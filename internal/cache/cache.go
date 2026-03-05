// Package cache provides a cache abstraction for storing model data.
// Supports both local (in-memory/file) and Redis backends for multi-instance deployments.
package cache

import (
	"context"
	"encoding/json"
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

// ModelCache represents the cached model data structure.
// Models are grouped by provider to avoid repeating shared fields (provider_type, owned_by)
// on every model entry.
type ModelCache struct {
	UpdatedAt     time.Time                `json:"updated_at"`
	Providers     map[string]CachedProvider `json:"providers"`
	// ModelListData holds the raw JSON model registry bytes for cache persistence,
	// allowing the registry to restore its full model list without re-fetching.
	ModelListData json.RawMessage `json:"model_list_data,omitempty"`
}

// CachedProvider holds shared fields for all models from a single provider.
type CachedProvider struct {
	ProviderType string        `json:"provider_type"`
	OwnedBy      string        `json:"owned_by"`
	Models       []CachedModel `json:"models"`
}

// CachedModel represents a single cached model entry within a provider group.
type CachedModel struct {
	ID      string `json:"id"`
	Created int64  `json:"created"`
}

// Cache defines the interface for model cache storage.
// Implementations must be safe for concurrent use.
type Cache interface {
	// Get retrieves the model cache data.
	// Returns nil, nil if no cache exists yet.
	Get(ctx context.Context) (*ModelCache, error)

	// Set stores the model cache data.
	Set(ctx context.Context, cache *ModelCache) error

	// Close releases any resources held by the cache.
	Close() error
}
