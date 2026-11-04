package providers

import (
	"maps"
	"slices"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
)

// providerSettings is the operator configuration that shapes one provider
// instance's inventory: what RegisterProviderWithNameAndType's callers
// otherwise apply with SetProviderConfiguredModels,
// SetProviderMetadataOverrides, and SetProviderModelFilter.
type providerSettings struct {
	configuredModels  []string
	metadataOverrides map[string]*core.ModelMetadata
	modelFilter       config.ModelFilter
}

// replaceProvider registers provider under providerName in place of whatever
// runs under that name now, together with its settings, in one step under the
// registry lock. A reader sees either the previous provider with its
// inventory or the replacement with the inventory it inherits, never the name
// without a provider or without models.
//
// The replacement takes the previous provider's place in the registration
// order, so a bare model ID several providers serve keeps resolving to the
// same one. When both have the same type, the replacement inherits the
// previous provider's discovered inventory, rebound to the new adapter and
// shaped by the new settings: an allowlist keeps only its models and a filter
// hides the models it excludes. A model the edit removes from the configured
// list is not inherited unless the previous inventory is known to come from
// the upstream listing alone, since it may exist only because it was
// configured. That inventory serves until the
// replacement's own model fetch publishes, which then drops whatever models
// the new upstream no longer lists; a failed fetch keeps it, marked stale,
// as for any provider whose refresh fails. A provider of another type starts
// without models, since the previous inventory says nothing about what it
// serves.
//
// Requests that already resolved the previous provider finish on it; only
// later lookups see the replacement. Fetches the previous provider started
// are discarded when they complete, because the stale-instance check in
// dropUnregisteredFetchedProvidersLocked compares provider instances.
func (r *ModelRegistry) replaceProvider(provider core.Provider, providerName, providerType string, settings providerSettings) {
	configuredModels := normalizeConfiguredProviderModels(settings.configuredModels)
	filter, filtered := newModelFilter(settings.modelFilter)
	overrides := make(map[string]*core.ModelMetadata, len(settings.metadataOverrides))
	for modelID, override := range settings.metadataOverrides {
		overrides[modelID] = override.Clone()
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	providerName, providerType = r.providerIdentityLocked(providerName, providerType)
	previousType, replaced := r.swapProviderLocked(provider, providerName, providerType)

	previous := r.discoveredByProvider[providerName]
	inherits := replaced && previousType == providerType && len(previous) > 0
	listing := inherits && r.inventoryIsListingLocked(providerName)
	if inherits && !listing {
		previous = withoutModels(previous, removedModels(r.configuredProviderModels[providerName], configuredModels))
	}

	setOrDelete(&r.configuredProviderModels, providerName, configuredModels, len(configuredModels) > 0)
	setOrDelete(&r.configMetadataOverrides, providerName, overrides, len(overrides) > 0)
	setOrDelete(&r.providerModelFilters, providerName, filter, filtered)

	// Rebinding runs even when every inherited model was a removed configured
	// one, so the edit's new configured models still serve.
	var inherited map[string]*ModelInfo
	if inherits {
		inherited = r.reboundInventoryLocked(previous, provider, providerName, providerType, listing)
	}
	if len(inherited) > 0 {
		r.discoveredByProvider[providerName] = inherited
		// The runtime state describes the inherited inventory, so it stays
		// until the replacement's first fetch overwrites it.
		state := r.providerRuntime[providerName]
		state.registered = true
		r.providerRuntime[providerName] = state
	} else {
		delete(r.discoveredByProvider, providerName)
		r.providerRuntime[providerName] = providerRuntimeState{registered: true}
	}
	r.publishFilteredInventoryLocked()
	r.invalidateSortedCaches()
}

// swapProviderLocked puts provider where the provider registered under
// providerName sits in the registration order, or appends it when nothing
// is, and reports the type of the provider it replaced. Caller must hold
// r.mu for writing.
func (r *ModelRegistry) swapProviderLocked(provider core.Provider, providerName, providerType string) (previousType string, replaced bool) {
	kept := make([]core.Provider, 0, len(r.providers)+1)
	for _, p := range r.providers {
		if r.providerNames[p] != providerName {
			kept = append(kept, p)
			continue
		}
		if !replaced {
			previousType, replaced = r.providerTypes[p], true
			kept = append(kept, provider)
		}
		delete(r.providerTypes, p)
		delete(r.providerNames, p)
	}
	if !replaced {
		kept = append(kept, provider)
	}
	r.providers = kept
	r.providerTypes[provider] = providerType
	r.providerNames[provider] = providerName
	return previousType, replaced
}

// reboundInventoryLocked returns the previous inventory of providerName
// rebound to provider, with the current configured models applied to it and
// its metadata enriched again from what the provider reported, so the
// result matches what a fetch returning the same models would publish.
// listing reports whether previous is exactly the upstream's last listing; when
// it is not, a fallback-mode configured list was serving in place of one, so
// the new configured models are added to what is inherited rather than
// ignored as they would be next to a successful listing.
// Caller must hold r.mu for writing.
func (r *ModelRegistry) reboundInventoryLocked(previous map[string]*ModelInfo, provider core.Provider, providerName, providerType string, listing bool) map[string]*ModelInfo {
	listed := &core.ModelsResponse{Object: "list", Data: make([]core.Model, 0, len(previous))}
	for _, info := range previous {
		if info == nil {
			continue
		}
		model := info.Model
		model.Metadata = info.Discovered.Clone()
		listed.Data = append(listed.Data, model)
	}
	mode := config.ResolveConfiguredProviderModelsMode(r.configuredProviderModelsMode)
	if mode == config.ConfiguredProviderModelsModeFallback && !listing {
		mode = config.ConfiguredProviderModelsModeMerge
	}
	resp, _ := applyConfiguredProviderModels(
		providerName,
		providerType,
		mode,
		r.configuredProviderModels[providerName],
		listed,
		nil,
		time.Now().Unix(),
	)
	rebound := map[string]map[string]*ModelInfo{
		providerName: modelInfoMapFromResponse(resp, provider, providerName, providerType),
	}
	if r.modelList != nil {
		enrichProviderModelMaps(r.modelList, r.providerTypes, rebound, nil)
	}
	applyConfigMetadataOverrides(r.configMetadataOverrides, rebound, nil)
	applyInferredModelMetadata(rebound, nil)
	return rebound[providerName]
}

// inventoryIsListingLocked reports whether providerName's inventory is
// exactly what its last /models listing returned: in fallback mode a
// successful listing is served as is, while every other source (an allowlist,
// a merge, a fallback after a failed or empty listing, a provider without a
// listing, the cache) may include configured models the upstream never
// listed. Caller must hold r.mu.
func (r *ModelRegistry) inventoryIsListingLocked(providerName string) bool {
	if config.ResolveConfiguredProviderModelsMode(r.configuredProviderModelsMode) != config.ConfiguredProviderModelsModeFallback {
		return false
	}
	state := r.providerRuntime[providerName]
	return !state.lastModelFetchAt.IsZero() &&
		state.lastModelFetchSuccessAt.Equal(state.lastModelFetchAt) &&
		!state.modelListingUnsupported &&
		!state.catalogStandIn
}

// removedModels returns the models in before that after no longer lists.
func removedModels(before, after []string) []string {
	var removed []string
	for _, modelID := range before {
		if !slices.Contains(after, modelID) {
			removed = append(removed, modelID)
		}
	}
	return removed
}

// withoutModels returns inventory without the given models, copying it only
// when one of them is present.
func withoutModels(inventory map[string]*ModelInfo, modelIDs []string) map[string]*ModelInfo {
	out, copied := inventory, false
	for _, modelID := range modelIDs {
		if _, ok := out[modelID]; !ok {
			continue
		}
		if !copied {
			out, copied = maps.Clone(inventory), true
		}
		delete(out, modelID)
	}
	return out
}

// setOrDelete stores value under key in *m when keep is true, allocating the
// map on first use, and deletes key otherwise.
func setOrDelete[V any](m *map[string]V, key string, value V, keep bool) {
	if !keep {
		delete(*m, key)
		return
	}
	if *m == nil {
		*m = make(map[string]V)
	}
	(*m)[key] = value
}
