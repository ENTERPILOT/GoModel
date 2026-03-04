// Package cache provides a cache abstraction for storing model data.
// Supports both local (in-memory/file) and Redis backends for multi-instance deployments.
package cache

import (
	"context"
	"encoding/json"
	"time"
)

// ModelCache represents the cached model data structure.
// This is the data that gets stored and retrieved from the cache.
type ModelCache struct {
	UpdatedAt     time.Time              `json:"updated_at"`
	Models        []CachedModel          `json:"models"`
	// ModelListData holds the raw JSON model registry bytes for cache persistence,
	// allowing the registry to restore its full model list without re-fetching.
	ModelListData json.RawMessage `json:"model_list_data,omitempty"`
}

// CachedModel represents a single cached model entry.
type CachedModel struct {
	ModelID      string `json:"model_id"`
	Provider     string `json:"provider"`
	ProviderType string `json:"provider_type"`
	Object       string `json:"object"`
	OwnedBy      string `json:"owned_by"`
	Created      int64  `json:"created"`
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
