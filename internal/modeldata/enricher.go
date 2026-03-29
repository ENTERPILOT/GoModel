package modeldata

import (
	"gomodel/internal/core"
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
}

// EnrichStats summarizes one metadata enrichment pass.
type EnrichStats struct {
	Enriched int
	Total    int
}

// Enrich iterates all models accessible via the accessor and attaches resolved
// metadata from the model list. Models not found in the list are left unchanged.
func Enrich(accessor ModelInfoAccessor, list *ModelList) EnrichStats {
	if list == nil || accessor == nil {
		return EnrichStats{}
	}

	var enriched int
	ids := accessor.ModelIDs()

	for _, modelID := range ids {
		providerType := accessor.GetProviderType(modelID)
		meta := Resolve(list, providerType, modelID)
		if meta != nil {
			accessor.SetMetadata(modelID, meta)
			enriched++
		}
	}

	return EnrichStats{
		Enriched: enriched,
		Total:    len(ids),
	}
}
