// Package modelcache provides model-specific cache types and interfaces.
// It defines the data structures for caching LLM provider model lists
// and the Cache interface that local and Redis backends implement.
package modelcache

import (
	"context"
	"encoding/json"
	"time"
)

// ModelCache represents the cached model data structure.
// Models are grouped by provider to avoid repeating shared fields (provider_type, owned_by)
// on every model entry.
type ModelCache struct {
	UpdatedAt time.Time                 `json:"updated_at"`
	Providers map[string]CachedProvider `json:"providers"`
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
