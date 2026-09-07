package providers

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/cache/modelcache"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/modeldata"
)

// LoadFromCache loads the model list from the cache backend.
// Returns the number of models loaded and any error encountered.
func (r *ModelRegistry) LoadFromCache(ctx context.Context) (int, error) {
	r.mu.RLock()
	cacheBackend := r.cache
	r.mu.RUnlock()

	if cacheBackend == nil {
		return 0, nil
	}

	modelCache, err := cacheBackend.Get(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to read cache: %w", err)
	}

	if modelCache == nil {
		return 0, nil // No cache yet, not an error
	}

	// Build lookup maps from configured providers.
	r.mu.RLock()
	nameToProvider := make(map[string]core.Provider, len(r.providerNames))
	nameToProviderType := make(map[string]string, len(r.providerNames))
	for provider, pName := range r.providerNames {
		nameToProvider[pName] = provider
		nameToProviderType[pName] = r.providerTypes[provider]
	}
	r.mu.RUnlock()

	// Populate the per-provider inventory from the grouped cache structure. The
	// global "first provider wins" map is derived from it when publishing.
	newModelsByProvider := make(map[string]map[string]*ModelInfo)
	cachedProviderTypes := make(map[string]string, len(modelCache.Providers))
	for providerName, cachedProv := range modelCache.Providers {
		provider, ok := nameToProvider[providerName]
		if !ok {
			// Provider not configured, skip all its models
			continue
		}
		cachedProviderTypes[providerName] = strings.TrimSpace(cachedProv.ProviderType)
		providerType := strings.TrimSpace(nameToProviderType[providerName])
		if providerType == "" {
			providerType = strings.TrimSpace(cachedProv.ProviderType)
		}
		providerModels := make(map[string]*ModelInfo, len(cachedProv.Models))
		for _, cached := range cachedProv.Models {
			// Restore what the provider reported for the model, so enrichment
			// merges the catalog under it exactly as it would after a live
			// fetch. Caches written before this field existed carry none, and
			// those models keep catalog-only metadata until the next refresh.
			info := newModelInfo(core.Model{
				ID:       cached.ID,
				Object:   "model",
				OwnedBy:  cachedProv.OwnedBy,
				Created:  cached.Created,
				Metadata: cached.Metadata.Clone(),
			}, provider, providerName, providerType)
			if _, exists := providerModels[info.Model.ID]; exists {
				// Trimmed duplicates collapse to one record; first wins.
				continue
			}
			providerModels[info.Model.ID] = info
		}
		newModelsByProvider[providerName] = providerModels
	}

	configuredProviderModels, configuredProviderModelsMode := r.snapshotConfiguredProviderModels()
	if len(configuredProviderModels) > 0 {
		for providerName, configuredModels := range configuredProviderModels {
			provider, ok := nameToProvider[providerName]
			if !ok {
				continue
			}
			providerType := strings.TrimSpace(nameToProviderType[providerName])
			if providerType == "" {
				providerType = strings.TrimSpace(cachedProviderTypes[providerName])
			}
			providerModels := newModelsByProvider[providerName]
			upstream := modelsResponseFromProviderMap(providerModels)
			resp, reason := applyConfiguredProviderModels(providerName, providerType, configuredProviderModelsMode, configuredModels, upstream, nil, modelCache.UpdatedAt.Unix())
			if reason == configuredProviderModelsNotApplied {
				continue
			}
			newModelsByProvider[providerName] = modelInfoMapFromResponse(resp, provider, providerName, providerType)
		}
	}

	// Load model list data from cache if available
	var list *modeldata.ModelList
	if len(modelCache.ModelListData) > 0 {
		parsed, parseErr := modeldata.Parse(modelCache.ModelListData)
		if parseErr != nil {
			slog.Warn("failed to parse cached model list data", "error", parseErr)
		} else {
			list = parsed
		}
	}

	// Enrich cached models with model list metadata
	metadataStats := metadataEnrichmentStats{}
	if list != nil {
		metadataStats = enrichProviderModelMaps(list, r.snapshotProviderTypes(), newModelsByProvider, nil)
	}
	configOverrides := r.snapshotConfigOverrides()
	metadataStats.Enriched += applyConfigMetadataOverrides(configOverrides, newModelsByProvider, nil)
	metadataStats.Enriched += applyInferredModelMetadata(newModelsByProvider, nil)

	r.mu.Lock()
	// Publishing applies model filters after enrichment, so price rules see the
	// cached pricing while the restored inventory itself stays unfiltered.
	r.discoveredByProvider = newModelsByProvider
	r.publishFilteredInventoryLocked()
	loadedModels := len(r.models)
	r.invalidateSortedCaches()
	if list != nil {
		r.modelList = list
		r.modelListRaw = modelCache.ModelListData
		r.setModelListValidatorLocked(modelCache.ModelListETag, modelCache.ModelListURL)
	}
	r.mu.Unlock()

	attrs := []any{
		"models", loadedModels,
		"cache_updated_at", modelCache.UpdatedAt,
	}
	attrs = append(attrs, metadataStats.slogAttrs()...)
	slog.Info("loaded models from cache", attrs...)

	return loadedModels, nil
}

// SaveToCache saves the current model list to the cache backend.
func (r *ModelRegistry) SaveToCache(ctx context.Context) error {
	r.mu.RLock()
	cacheBackend := r.cache
	// Persist the unfiltered inventory: model filters are a view over what a
	// provider serves, so a restart with a loosened filter must be able to
	// re-admit models without waiting for a successful upstream fetch.
	modelsByProvider := make(map[string]map[string]*ModelInfo, len(r.discoveredByProvider))
	for providerName, models := range r.discoveredByProvider {
		// A stale inventory was carried forward from before the provider went
		// offline; persisting it would resurrect the offline provider's models
		// on every restart. The provider re-enters the cache once a refresh
		// succeeds again.
		if r.providerRuntime[providerName].inventoryStale {
			continue
		}
		modelsByProvider[providerName] = make(map[string]*ModelInfo, len(models))
		maps.Copy(modelsByProvider[providerName], models)
	}
	providerTypes := make(map[core.Provider]string, len(r.providerTypes))
	maps.Copy(providerTypes, r.providerTypes)
	modelListRaw := r.modelListRaw
	modelListETag := r.modelListETag
	modelListETagURL := r.modelListETagURL
	r.mu.RUnlock()

	if cacheBackend == nil {
		return nil
	}

	mc := &modelcache.ModelCache{
		UpdatedAt:     time.Now().UTC(),
		Providers:     make(map[string]modelcache.CachedProvider, len(modelsByProvider)),
		ModelListData: modelListRaw,
		ModelListETag: modelListETag,
		ModelListURL:  modelListETagURL,
	}

	var totalModels int
	for providerName, models := range modelsByProvider {
		// Determine provider type and owned_by from any model in this provider group.
		var pType, ownedBy string
		for _, info := range models {
			if ownedBy == "" {
				ownedBy = info.Model.OwnedBy
			}
			if pType == "" {
				pType = strings.TrimSpace(info.ProviderType)
				if pType == "" {
					pType = strings.TrimSpace(providerTypes[info.Provider])
				}
			}
			if pType != "" && ownedBy != "" {
				break
			}
		}
		if pType == "" {
			// No known provider type for this provider, skip entirely.
			continue
		}

		modelIDs := make([]string, 0, len(models))
		for modelID := range models {
			modelIDs = append(modelIDs, modelID)
		}
		sort.Strings(modelIDs)

		cachedModels := make([]modelcache.CachedModel, 0, len(modelIDs))
		for _, modelID := range modelIDs {
			info := models[modelID]
			cachedModels = append(cachedModels, modelcache.CachedModel{
				ID:      modelID,
				Created: info.Model.Created,
				// Persist the provider's own report, not Model.Metadata: that
				// one carries whatever the catalog contributed at enrichment
				// time, and storing it would pin a stale catalog entry across
				// restarts instead of letting each pass re-merge.
				Metadata: info.Discovered.Clone(),
			})
		}
		mc.Providers[providerName] = modelcache.CachedProvider{
			ProviderType: pType,
			OwnedBy:      ownedBy,
			Models:       cachedModels,
		}
		totalModels += len(cachedModels)
	}

	if err := cacheBackend.Set(ctx, mc); err != nil {
		return fmt.Errorf("failed to save cache: %w", err)
	}

	slog.Debug("saved models to cache", "models", totalModels)
	return nil
}
