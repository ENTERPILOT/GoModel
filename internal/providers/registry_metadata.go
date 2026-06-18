package providers

import (
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/goccy/go-json"

	"gomodel/config"
	"gomodel/internal/core"
	"gomodel/internal/modeldata"
)

// SetModelList stores the parsed model list and its raw bytes for cache persistence.
func (r *ModelRegistry) SetModelList(list *modeldata.ModelList, raw json.RawMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.modelList = list
	r.modelListRaw = raw
}

// EnrichModels re-applies model list metadata to all currently registered models.
// Call this after SetModelList to update existing models with the new metadata.
// Holds the write lock for the entire operation and replaces published ModelInfo
// entries instead of mutating them in place so concurrent readers can safely keep
// using older snapshots after unlocking.
func (r *ModelRegistry) EnrichModels() {
	_ = r.enrichModels()
}

func (r *ModelRegistry) enrichModels() metadataEnrichmentStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.enrichModelsLocked()
}

func (r *ModelRegistry) enrichModelsLocked() metadataEnrichmentStats {
	if len(r.models) == 0 {
		return metadataEnrichmentStats{}
	}
	if r.modelList == nil && len(r.configMetadataOverrides) == 0 {
		return metadataEnrichmentStats{}
	}

	providerTypes := make(map[core.Provider]string, len(r.providerTypes))
	maps.Copy(providerTypes, r.providerTypes)

	replacements := make(map[*ModelInfo]*ModelInfo, len(r.models))
	stats := metadataEnrichmentStats{}
	if r.modelList != nil {
		stats = enrichProviderModelMaps(r.modelList, providerTypes, r.modelsByProvider, replacements)
	}
	stats.Enriched += applyConfigMetadataOverrides(r.configMetadataOverrides, r.modelsByProvider, replacements)
	for modelID, info := range r.models {
		if replacement, ok := replacements[info]; ok {
			r.models[modelID] = replacement
		}
	}
	r.invalidateSortedCaches()
	return stats
}

func (r *ModelRegistry) setModelListAndEnrich(list *modeldata.ModelList, raw json.RawMessage) metadataEnrichmentStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.modelList = list
	r.modelListRaw = raw
	return r.enrichModelsLocked()
}

// ResolveMetadata resolves metadata for a model directly via the stored model list,
// bypassing the registry key lookup. This handles cases where the usage DB stores
// a response model ID (e.g., "gpt-4o-2024-08-06") that differs from the registry
// key (e.g., "gpt-4o") by using the reverse index in the model list.
func (r *ModelRegistry) ResolveMetadata(providerType, modelID string) *core.ModelMetadata {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.modelList == nil {
		return nil
	}
	return modeldata.Resolve(r.modelList, providerType, modelID)
}

// GetModelMetadata returns the metadata for a model, or nil if not found or not enriched.
func (r *ModelRegistry) GetModelMetadata(modelID string) *core.ModelMetadata {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if info, ok := r.models[modelID]; ok {
		return info.Model.Metadata
	}
	return nil
}

// ResolvePricing returns the pricing metadata for a model, trying the registry first
// and falling back to a reverse-index lookup via the model list.
// Returns nil if no pricing is available.
func (r *ModelRegistry) ResolvePricing(model, providerType string) *core.ModelPricing {
	providerSelector := strings.TrimSpace(providerType)
	if meta := r.getProviderModelMetadata(providerSelector, model); meta != nil && meta.Pricing != nil {
		return meta.Pricing
	}

	meta := r.GetModelMetadata(model)
	if meta != nil && meta.Pricing != nil {
		return meta.Pricing
	}
	if providerSelector != "" {
		meta = r.ResolveMetadata(r.metadataProviderType(providerSelector), r.metadataModelID(model))
		if meta != nil && meta.Pricing != nil {
			return meta.Pricing
		}
	}
	return nil
}

func (r *ModelRegistry) getProviderModelMetadata(providerSelector, model string) *core.ModelMetadata {
	providerSelector = strings.TrimSpace(providerSelector)
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}

	modelProviderName, modelID := splitModelSelector(model)
	r.mu.RLock()
	defer r.mu.RUnlock()

	if modelProviderName != "" {
		if meta := metadataFromProviderModel(r.modelsByProvider[modelProviderName], modelID, model); meta != nil {
			return meta
		}
		if r.hasConfiguredProviderNameLocked(modelProviderName) {
			return nil
		}
	}

	if providerSelector == "" {
		return nil
	}
	if meta := metadataFromProviderModel(r.modelsByProvider[providerSelector], model); meta != nil {
		return meta
	}
	return nil
}

func metadataFromProviderModel(providerModels map[string]*ModelInfo, model string, alternates ...string) *core.ModelMetadata {
	if len(providerModels) == 0 {
		return nil
	}
	candidates := append([]string{model}, alternates...)
	for _, candidate := range candidates {
		info := providerModels[strings.TrimSpace(candidate)]
		if info == nil || info.Model.Metadata == nil {
			continue
		}
		return info.Model.Metadata
	}
	return nil
}

func (r *ModelRegistry) metadataProviderType(providerSelector string) string {
	providerSelector = strings.TrimSpace(providerSelector)
	if providerSelector == "" {
		return ""
	}
	if providerType := r.GetProviderTypeForName(providerSelector); providerType != "" {
		return providerType
	}
	return providerSelector
}

func (r *ModelRegistry) metadataModelID(model string) string {
	model = strings.TrimSpace(model)
	providerName, modelID := splitModelSelector(model)
	if providerName == "" {
		return model
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.hasConfiguredProviderNameLocked(providerName) {
		providerModels := r.modelsByProvider[providerName]
		if _, ok := providerModels[modelID]; ok {
			return modelID
		}
		if _, ok := providerModels[model]; ok {
			return model
		}
		return modelID
	}
	return model
}

// snapshotProviderTypes returns a copy of the providerTypes map for use outside the lock.
func (r *ModelRegistry) snapshotProviderTypes() map[core.Provider]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m := make(map[core.Provider]string, len(r.providerTypes))
	maps.Copy(m, r.providerTypes)
	return m
}

// snapshotConfigOverrides returns a copy of the configMetadataOverrides outer
// and inner maps for use outside the lock. The inner *core.ModelMetadata
// pointers are shared, which is safe because SetProviderMetadataOverrides
// deep-clones on insertion and the registry never hands those values back out.
func (r *ModelRegistry) snapshotConfigOverrides() map[string]map[string]*core.ModelMetadata {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.configMetadataOverrides) == 0 {
		return nil
	}
	out := make(map[string]map[string]*core.ModelMetadata, len(r.configMetadataOverrides))
	for provider, inner := range r.configMetadataOverrides {
		innerCopy := make(map[string]*core.ModelMetadata, len(inner))
		for modelID, meta := range inner {
			innerCopy[modelID] = meta
		}
		out[provider] = innerCopy
	}
	return out
}

func (r *ModelRegistry) snapshotConfiguredProviderModels() (map[string][]string, config.ConfiguredProviderModelsMode) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	mode := config.ResolveConfiguredProviderModelsMode(r.configuredProviderModelsMode)
	if len(r.configuredProviderModels) == 0 {
		return nil, mode
	}
	out := make(map[string][]string, len(r.configuredProviderModels))
	for provider, models := range r.configuredProviderModels {
		out[provider] = slices.Clone(models)
	}
	return out, mode
}

// collectionEmpty reports whether a reflect.Value representing a slice, array,
// or map has no elements (covering both nil and non-nil-but-zero-length), and
// falls back to reflect.Value.IsZero for other kinds. This lets override-
// emptiness checks treat `modes: []` the same as an omitted field, which
// IsZero alone would not.
func collectionEmpty(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Slice, reflect.Map, reflect.Array:
		return v.Len() == 0
	}
	return v.IsZero()
}

// structFieldsEmpty returns true if every field of the given struct value
// passes collectionEmpty.
func structFieldsEmpty(v reflect.Value) bool {
	for i := 0; i < v.NumField(); i++ {
		if !collectionEmpty(v.Field(i)) {
			return false
		}
	}
	return true
}

// metadataOverrideEmpty reports whether an override has no effective content.
// An empty override (either nil or zero-valued on every field) would turn a
// nil current metadata into a non-nil empty struct after MergeMetadata, so
// callers should short-circuit on it. Uses reflect-based field inspection so
// new fields on core.ModelMetadata are picked up automatically; Pricing is
// handled separately so a non-nil pointer to an empty pricing block still
// counts as empty.
func metadataOverrideEmpty(m *core.ModelMetadata) bool {
	if m == nil {
		return true
	}
	if !pricingOverrideEmpty(m.Pricing) {
		return false
	}
	tmp := *m
	tmp.Pricing = nil
	return structFieldsEmpty(reflect.ValueOf(tmp))
}

// pricingOverrideEmpty reports whether a pricing override has no effective
// content — nil or every field at its zero value (with collections treated as
// empty when length==0).
func pricingOverrideEmpty(p *core.ModelPricing) bool {
	if p == nil {
		return true
	}
	return structFieldsEmpty(reflect.ValueOf(*p))
}

// applyConfigMetadataOverrides layers operator-declared metadata onto already-
// enriched models. Call it after enrichProviderModelMaps with the same
// replacements map (pass nil replacements for fresh, unpublished maps).
// Returns the number of models whose metadata was updated.
func applyConfigMetadataOverrides(
	overrides map[string]map[string]*core.ModelMetadata,
	modelsByProvider map[string]map[string]*ModelInfo,
	replacements map[*ModelInfo]*ModelInfo,
) int {
	if len(overrides) == 0 {
		return 0
	}
	// reverse lets us find the pre-enrichment pointer when an entry has
	// already been replaced by enrichProviderModelMaps, so our replacement
	// chain stays consistent from the caller's perspective. Always allocated
	// when replacements is non-nil so the else-branch write below cannot hit
	// a nil map when enrichment made no replacements.
	var reverse map[*ModelInfo]*ModelInfo
	if replacements != nil {
		reverse = make(map[*ModelInfo]*ModelInfo, len(replacements))
		for orig, repl := range replacements {
			reverse[repl] = orig
		}
	}
	applied := 0
	for providerName, modelOverrides := range overrides {
		providerModels, ok := modelsByProvider[providerName]
		if !ok {
			continue
		}
		for modelID, override := range modelOverrides {
			if metadataOverrideEmpty(override) {
				// A nil or effectively-empty override has nothing to
				// contribute. Skipping avoids turning a nil current metadata
				// into a non-nil empty struct, which the DeepEqual check
				// below would not catch.
				continue
			}
			current, ok := providerModels[modelID]
			if !ok {
				continue
			}
			basePricing := metadataPricing(current.Model.Metadata)
			basePricingSources := metadataPricingSources(current.Model.Metadata)
			merged := modeldata.MergeMetadata(current.Model.Metadata, override)
			if override.Pricing != nil {
				merged.Pricing = mergeConfigPricing(basePricing, override.Pricing)
				merged.PricingSources = mergePricingSources(basePricingSources, override.Pricing.FieldSources(core.ModelPricingSourceConfigYAML))
			}
			// Skip no-op merges so concurrent readers holding the current
			// pointer keep a stable view when the override adds no new info.
			if reflect.DeepEqual(current.Model.Metadata, merged) {
				continue
			}
			if replacements == nil {
				current.Model.Metadata = merged
				applied++
				continue
			}
			cloned := *current
			cloned.Model.Metadata = merged
			next := &cloned
			providerModels[modelID] = next
			if orig, hasOrig := reverse[current]; hasOrig {
				replacements[orig] = next
				reverse[next] = orig
			} else {
				replacements[current] = next
				reverse[next] = current
			}
			applied++
		}
	}
	return applied
}

func enrichProviderModelMaps(
	list *modeldata.ModelList,
	providerTypes map[core.Provider]string,
	modelsByProvider map[string]map[string]*ModelInfo,
	replacements map[*ModelInfo]*ModelInfo,
) metadataEnrichmentStats {
	if list == nil {
		return metadataEnrichmentStats{}
	}
	stats := metadataEnrichmentStats{}
	for _, providerModels := range modelsByProvider {
		if len(providerModels) == 0 {
			continue
		}
		stats.Providers++
		accessor := &registryAccessor{
			models:        providerModels,
			providerTypes: providerTypes,
			replacements:  replacements,
		}
		enrichStats := modeldata.Enrich(accessor, list)
		stats.Enriched += enrichStats.Enriched
		stats.Total += enrichStats.Total
	}
	return stats
}

// registryAccessor implements modeldata.ModelInfoAccessor.
// The models map may be either an unpublished snapshot (Initialize, LoadFromCache)
// or the live registry map (EnrichModels, which uses replacements to preserve
// immutability of already-published ModelInfo values).
type registryAccessor struct {
	models        map[string]*ModelInfo
	providerTypes map[core.Provider]string
	replacements  map[*ModelInfo]*ModelInfo
}

func (a *registryAccessor) ModelIDs() []string {
	ids := make([]string, 0, len(a.models))
	for id := range a.models {
		ids = append(ids, id)
	}
	return ids
}

func (a *registryAccessor) GetProviderType(modelID string) string {
	info, ok := a.models[modelID]
	if !ok {
		return ""
	}
	if providerType := strings.TrimSpace(info.ProviderType); providerType != "" {
		return providerType
	}
	return strings.TrimSpace(a.providerTypes[info.Provider])
}

func (a *registryAccessor) SetMetadata(modelID string, meta *core.ModelMetadata) {
	if info, ok := a.models[modelID]; ok {
		if a.replacements != nil {
			cloned := *info
			cloned.Model.Metadata = meta
			replacement := &cloned
			a.models[modelID] = replacement
			a.replacements[info] = replacement
			return
		}
		info.Model.Metadata = meta
	}
}
