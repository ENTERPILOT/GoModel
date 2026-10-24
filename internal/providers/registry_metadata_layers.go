package providers

import (
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/modeldata"
)

// ModelMetadataLayers is the provenance view of one model's metadata: the
// merged result the gateway serves plus each layer it was merged from. A nil
// layer means that source knows nothing about the model.
type ModelMetadataLayers struct {
	Selector string `json:"selector"`
	// Effective is the merged metadata, as listed by /admin/models.
	Effective *core.ModelMetadata `json:"effective"`
	// Provider is what the provider reported in its own model listing.
	Provider *core.ModelMetadata `json:"provider" extensions:"x-nullable"`
	// Catalog is the model list (ai-model-list) entry.
	Catalog *core.ModelMetadata `json:"catalog" extensions:"x-nullable"`
	// Config is the config.yaml metadata override.
	Config *core.ModelMetadata `json:"config" extensions:"x-nullable"`
	// Sources names the layer that supplied each effective field
	// (see modeldata.MetadataSources).
	Sources map[string]string `json:"sources"`
}

// ModelMetadataLayers returns the metadata layers of one routable model.
// providerSegment is a provider instance name or a provider type, modelID the
// raw upstream model ID. Nothing is stored for this view: the provider's
// report and the config override are already held per model, and the catalog
// entry is resolved on demand, so the cost is one read lock and a few lookups
// per call.
func (r *ModelRegistry) ModelMetadataLayers(providerSegment, modelID string) (ModelMetadataLayers, bool) {
	selector, ok := r.ResolveProviderSelector(providerSegment, modelID)
	if !ok {
		return ModelMetadataLayers{}, false
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	info := r.modelsByProvider[selector.Provider][selector.Model]
	if info == nil {
		return ModelMetadataLayers{}, false
	}
	providerType := info.ProviderType
	if providerType == "" {
		providerType = r.providerTypes[info.Provider]
	}

	var catalog *core.ModelMetadata
	if r.modelList != nil {
		catalog = modeldata.Resolve(r.modelList, providerType, selector.Model).Clone()
	}
	var configOverride *core.ModelMetadata
	if override := r.configMetadataOverrides[selector.Provider][selector.Model]; !metadataOverrideEmpty(override) {
		configOverride = override.Clone()
	}

	return ModelMetadataLayers{
		Selector:  selector.QualifiedModel(),
		Effective: info.Model.Metadata.Clone(),
		Provider:  info.Discovered.Clone(),
		Catalog:   catalog,
		Config:    configOverride,
		Sources:   modeldata.MetadataSources(info.Model.Metadata, info.Discovered, catalog, configOverride),
	}, true
}
