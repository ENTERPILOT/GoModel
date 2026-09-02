// Package providers provides model registry and routing for LLM providers.
package providers

import (
	"fmt"
	"maps"
	"strings"
	"sync"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/cache/modelcache"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/modeldata"
)

// ModelInfo holds information about a model and its provider
type ModelInfo struct {
	Model        core.Model
	Provider     core.Provider
	ProviderName string
	ProviderType string
	// Discovered is the metadata the provider itself reported for this model,
	// held apart from Model.Metadata because enrichment rewrites that field on
	// every catalog refresh. Keeping the pristine value lets each refresh
	// recompute the merge from the same inputs rather than layering onto its own
	// previous output, which would otherwise pin stale catalog data in place.
	Discovered *core.ModelMetadata
}

// newModelInfo registers a model together with the metadata its provider
// reported for it. Always build ModelInfo through this so Discovered is
// captured before enrichment has a chance to overwrite Model.Metadata.
//
// The model ID, provider name, and provider type are whitespace-trimmed here,
// at the boundary where inventory enters the registry, so every read path can
// compare stored values directly.
func newModelInfo(model core.Model, provider core.Provider, providerName, providerType string) *ModelInfo {
	model.ID = strings.TrimSpace(model.ID)
	return &ModelInfo{
		Model:        model,
		Provider:     provider,
		ProviderName: strings.TrimSpace(providerName),
		ProviderType: strings.TrimSpace(providerType),
		Discovered:   model.Metadata.Clone(),
	}
}

// ModelRegistry manages the mapping of models to their providers.
// It fetches models from providers on startup and caches them in memory.
// Supports loading from a cache (local file or Redis) for instant startup.
type ModelRegistry struct {
	mu     sync.RWMutex
	models map[string]*ModelInfo // model ID -> model info (first provider wins)
	// modelsByProvider is the ROUTABLE catalog: discoveredByProvider with each
	// provider's model filter applied. Everything that serves, lists, or
	// resolves a model reads this map.
	modelsByProvider map[string]map[string]*ModelInfo // provider instance name -> model ID -> model info
	// discoveredByProvider is the unfiltered inventory as fetched or restored
	// from cache, before model filters. It is what enrichment runs over and
	// what is persisted, so a model a filter currently rejects returns as soon
	// as its pricing or the filter changes, without waiting for a refetch.
	// Providers without a filter share their inner map with modelsByProvider.
	discoveredByProvider map[string]map[string]*ModelInfo
	providers            []core.Provider
	providerTypes        map[core.Provider]string // provider -> type string
	providerNames        map[core.Provider]string // provider -> configured provider instance name
	providerRuntime      map[string]providerRuntimeState
	cache                modelcache.Cache     // cache backend (local or redis)
	initialized          bool                 // true when at least one successful network fetch completed
	initMu               sync.Mutex           // protects initialized flag
	refreshCh            chan struct{}        // serializes provider/model-list refresh cycles
	refreshOnce          sync.Once            // initializes refreshCh for zero-value safety
	modelList            *modeldata.ModelList // parsed model list (nil = not loaded)
	modelListRaw         json.RawMessage      // raw bytes for cache persistence
	// modelListETag is the validator for conditional refetches and
	// modelListETagURL the URL it was issued by; the validator is only sent
	// back to that same URL, so a reconfigured MODEL_LIST_URL always fetches
	// unconditionally. Empty etag = fetch unconditionally.
	modelListETag    string
	modelListETagURL string
	// configMetadataOverrides holds operator-supplied metadata keyed by provider
	// instance name -> raw model ID. Applied after remote-registry enrichment as
	// a higher-priority layer. nil if no overrides declared.
	configMetadataOverrides map[string]map[string]*core.ModelMetadata
	// configuredProviderModels holds operator-supplied model inventories keyed by
	// configured provider instance name. The mode decides whether these entries
	// are fallback-only or an allowlist over the discovered upstream inventory.
	configuredProviderModels     map[string][]string
	configuredProviderModelsMode config.ConfiguredProviderModelsMode
	// providerModelFilters holds operator-supplied inventory filters keyed by
	// configured provider instance name. Applied after metadata enrichment;
	// providers without a filter are absent from the map.
	providerModelFilters map[string]modelFilter

	// Cached sorted slices, rebuilt lazily after models change.
	// nil means cache needs rebuilding. Protected by mu.
	sortedModels             []core.Model
	sortedModelsWithProvider []ModelWithProvider
	categoryCache            map[core.ModelCategory][]ModelWithProvider

	// Lazy O(1) resolution index from qualified selector keys ("<segment>/<id>")
	// to concrete provider-name-qualified selectors. qualifiedByName is keyed by
	// provider instance name, qualifiedByType by provider type. nil means the
	// index needs rebuilding; both maps are built together and cleared by
	// invalidateSortedCaches whenever the catalog changes. Protected by mu.
	qualifiedByName map[string]core.ModelSelector
	qualifiedByType map[string]core.ModelSelector
}

type metadataEnrichmentStats struct {
	Enriched  int
	Total     int
	Providers int
}

func (s metadataEnrichmentStats) slogAttrs() []any {
	return []any{
		"metadata_enriched", s.Enriched,
		"metadata_total", s.Total,
		"metadata_providers", s.Providers,
	}
}

// NewModelRegistry creates a new model registry
func NewModelRegistry() *ModelRegistry {
	return &ModelRegistry{
		models:                       make(map[string]*ModelInfo),
		modelsByProvider:             make(map[string]map[string]*ModelInfo),
		discoveredByProvider:         make(map[string]map[string]*ModelInfo),
		providerTypes:                make(map[core.Provider]string),
		providerNames:                make(map[core.Provider]string),
		providerRuntime:              make(map[string]providerRuntimeState),
		refreshCh:                    make(chan struct{}, 1),
		configuredProviderModelsMode: config.ConfiguredProviderModelsModeFallback,
	}
}

// SetCache sets the cache backend for persistent model storage.
// The cache can be a local file-based cache or a Redis cache.
func (r *ModelRegistry) SetCache(c modelcache.Cache) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache = c
}

// invalidateSortedCaches clears cached sorted slices so they are rebuilt lazily.
// Must be called while holding the write lock (r.mu.Lock).
func (r *ModelRegistry) invalidateSortedCaches() {
	r.sortedModels = nil
	r.sortedModelsWithProvider = nil
	r.categoryCache = nil
	r.qualifiedByName = nil
	r.qualifiedByType = nil
}

// RegisterProvider adds a provider to the registry
func (r *ModelRegistry) RegisterProvider(provider core.Provider) {
	r.RegisterProviderWithNameAndType(provider, "", "")
}

// RegisterProviderWithType adds a provider to the registry with its type string.
// The type is used for cache persistence to re-associate models with providers on startup.
func (r *ModelRegistry) RegisterProviderWithType(provider core.Provider, providerType string) {
	r.RegisterProviderWithNameAndType(provider, "", providerType)
}

// SetProviderMetadataOverrides records per-model metadata overrides declared in
// config.yaml for the given provider instance name. Overrides are merged onto
// remote-registry enrichment each time the registry re-enriches its models.
//
// Call with an empty/nil map to clear any prior overrides for that provider.
func (r *ModelRegistry) SetProviderMetadataOverrides(providerName string, overrides map[string]*core.ModelMetadata) {
	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(overrides) == 0 {
		delete(r.configMetadataOverrides, providerName)
		return
	}
	if r.configMetadataOverrides == nil {
		r.configMetadataOverrides = make(map[string]map[string]*core.ModelMetadata)
	}
	clone := make(map[string]*core.ModelMetadata, len(overrides))
	for k, v := range overrides {
		clone[k] = v.Clone()
	}
	r.configMetadataOverrides[providerName] = clone
}

// SetConfiguredProviderModelsMode controls how configured provider model lists
// affect the final registry inventory.
func (r *ModelRegistry) SetConfiguredProviderModelsMode(mode config.ConfiguredProviderModelsMode) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.configuredProviderModelsMode = config.ResolveConfiguredProviderModelsMode(mode)
}

// SetProviderConfiguredModels records the explicit model inventory declared for
// a configured provider instance. Call with an empty/nil slice to clear it.
func (r *ModelRegistry) SetProviderConfiguredModels(providerName string, models []string) {
	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		return
	}
	normalized := normalizeConfiguredProviderModels(models)
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(normalized) == 0 {
		delete(r.configuredProviderModels, providerName)
		return
	}
	if r.configuredProviderModels == nil {
		r.configuredProviderModels = make(map[string][]string)
	}
	r.configuredProviderModels[providerName] = normalized
}

// SetProviderModelFilter records the inventory filter declared for a configured
// provider instance. Call with an empty filter to clear it.
func (r *ModelRegistry) SetProviderModelFilter(providerName string, filter config.ModelFilter) {
	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		return
	}
	resolved, ok := newModelFilter(filter)
	r.mu.Lock()
	defer r.mu.Unlock()
	if !ok {
		delete(r.providerModelFilters, providerName)
	} else {
		if r.providerModelFilters == nil {
			r.providerModelFilters = make(map[string]modelFilter)
		}
		r.providerModelFilters[providerName] = resolved
	}
	// Republish so a filter set or cleared after the catalog loaded takes effect
	// immediately rather than at the next refresh.
	if len(r.discoveredByProvider) > 0 {
		r.publishFilteredInventoryLocked()
		r.invalidateSortedCaches()
	}
}

// snapshotProviderModelFilters copies the configured filters under a read lock.
func (r *ModelRegistry) snapshotProviderModelFilters() map[string]modelFilter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.providerModelFilters) == 0 {
		return nil
	}
	out := make(map[string]modelFilter, len(r.providerModelFilters))
	maps.Copy(out, r.providerModelFilters)
	return out
}

// RegisterProviderWithNameAndType adds a provider with a configured provider instance name and type.
// Name is used for unambiguous provider/model selection (e.g. "provider/model") and cache persistence.
func (r *ModelRegistry) RegisterProviderWithNameAndType(provider core.Provider, providerName, providerType string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	providerName = strings.TrimSpace(providerName)
	providerType = strings.TrimSpace(providerType)
	if providerName == "" {
		if providerType != "" {
			providerName = providerType
		} else {
			providerName = fmt.Sprintf("provider-%d", len(r.providers)+1)
		}
	}

	r.providers = append(r.providers, provider)
	r.providerTypes[provider] = providerType
	r.providerNames[provider] = providerName

	state := r.providerRuntime[providerName]
	state.registered = true
	r.providerRuntime[providerName] = state
}

// UnregisterProvider removes every provider instance registered under name
// (normally at most one) and immediately removes its models from the live
// catalog. Callers may still Refresh afterward to update the remaining
// providers, but a failed or slow refresh must not leave deleted-provider
// models visible. Safe to call for a name that was never registered.
func (r *ModelRegistry) UnregisterProvider(providerName string) {
	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	kept := make([]core.Provider, 0, len(r.providers))
	for _, p := range r.providers {
		if r.providerNames[p] != providerName {
			kept = append(kept, p)
			continue
		}
		delete(r.providerTypes, p)
		delete(r.providerNames, p)
	}
	r.providers = kept

	delete(r.providerRuntime, providerName)
	delete(r.configMetadataOverrides, providerName)
	delete(r.configuredProviderModels, providerName)
	delete(r.providerModelFilters, providerName)
	delete(r.discoveredByProvider, providerName)
	r.publishFilteredInventoryLocked()
	r.invalidateSortedCaches()
}
