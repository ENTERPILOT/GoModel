// Package modelcache provides model-specific cache types and interfaces.
// It defines the data structures for caching LLM provider model lists
// and the Cache interface that local and Redis backends implement.
package modelcache

import (
	"context"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
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
	// ModelListETag is the HTTP validator ModelListData was downloaded with,
	// letting the next fetch skip the download when the list is unchanged.
	// ModelListURL records which URL issued it, so the validator is never
	// presented to a reconfigured model list URL.
	ModelListETag string `json:"model_list_etag,omitempty"`
	ModelListURL  string `json:"model_list_url,omitempty"`
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
	// Metadata is what the provider itself reported about the model at
	// discovery — never the catalog-enriched result. A restart restores it as
	// the model's discovered metadata, so a provider that is slow or briefly
	// unreachable does not fall back to catalog-only values (or to none) for
	// its real context window, output limit, modes, and capabilities.
	Metadata *core.ModelMetadata `json:"metadata,omitempty"`
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
