package modeldata

import (
	"github.com/enterpilot/gomodel/internal/core"
)

// ModelInfoAccessor provides the minimal interface needed by Enrich to access
// and update model information. This avoids a circular dependency on the
// providers package.
type ModelInfoAccessor interface {
	// ModelIDs returns all registered model IDs.
	ModelIDs() []string
	// GetProviderType returns the provider type for a model ID.
	GetProviderType(modelID string) string
	// SetMetadata sets the metadata for a model ID.
	SetMetadata(modelID string, meta *core.ModelMetadata)
	// DiscoveredMetadata returns the metadata the provider itself reported for
	// a model, or nil when it reported none. It must keep returning the same
	// value across enrichment passes: Enrich merges onto it rather than onto
	// its own previous result, which is what keeps repeated passes idempotent.
	DiscoveredMetadata(modelID string) *core.ModelMetadata
}

// EnrichStats summarizes one metadata enrichment pass.
type EnrichStats struct {
	Enriched int
	Total    int
}

// Enrich iterates all models accessible via the accessor and merges resolved
// catalog metadata into each one. Models the catalog does not know are left
// unchanged.
//
// The catalog is the base and the provider's own report the override, field by
// field: a running provider describes its actual deployment (a local server's
// real context window, an API's live capability flags) better than a static
// registry can, while the catalog still supplies everything the provider never
// reports — display names, pricing, rankings.
func Enrich(accessor ModelInfoAccessor, list *ModelList) EnrichStats {
	if list == nil || accessor == nil {
		return EnrichStats{}
	}

	var enriched int
	ids := accessor.ModelIDs()

	for _, modelID := range ids {
		providerType := accessor.GetProviderType(modelID)
		catalog := Resolve(list, providerType, modelID)
		if catalog == nil {
			// Nothing in the catalog: whatever the provider discovered stands.
			continue
		}
		accessor.SetMetadata(modelID, MergeMetadata(catalog, accessor.DiscoveredMetadata(modelID)))
		enriched++
	}

	return EnrichStats{
		Enriched: enriched,
		Total:    len(ids),
	}
}
