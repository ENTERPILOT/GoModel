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
	providerOrderNames := make([]string, 0, len(r.providers))
	for _, provider := range r.providers {
		providerName := r.providerNames[provider]
		if providerName == "" {
			continue
		}
		providerOrderNames = append(providerOrderNames, providerName)
	}
	for provider, pName := range r.providerNames {
		nameToProvider[pName] = provider
		nameToProviderType[pName] = r.providerTypes[provider]
	}
	r.mu.RUnlock()

	// Populate model maps from grouped cache structure. Unqualified lookups keep "first provider wins".
	newModels := make(map[string]*ModelInfo)
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
			info := &ModelInfo{
				Model: core.Model{
					ID:      cached.ID,
					Object:  "model",
					OwnedBy: cachedProv.OwnedBy,
					Created: cached.Created,
				},
				Provider:     provider,
				ProviderName: providerName,
				ProviderType: providerType,
			}
			providerModels[cached.ID] = info
			if _, exists := newModels[cached.ID]; !exists {
				newModels[cached.ID] = info
			}
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
	newModels = rebuildGlobalModelMap(newModelsByProvider, providerOrderNames)

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

	r.mu.Lock()
	r.models = newModels
	r.modelsByProvider = newModelsByProvider
	r.invalidateSortedCaches()
	if list != nil {
		r.modelList = list
		r.modelListRaw = modelCache.ModelListData
	}
	r.mu.Unlock()

	attrs := []any{
		"models", len(newModels),
		"cache_updated_at", modelCache.UpdatedAt,
	}
	attrs = append(attrs, metadataStats.slogAttrs()...)
	slog.Info("loaded models from cache", attrs...)

	return len(newModels), nil
}

// SaveToCache saves the current model list to the cache backend.
func (r *ModelRegistry) SaveToCache(ctx context.Context) error {
	r.mu.RLock()
	cacheBackend := r.cache
	modelsByProvider := make(map[string]map[string]*ModelInfo, len(r.modelsByProvider))
	for providerName, models := range r.modelsByProvider {
		modelsByProvider[providerName] = make(map[string]*ModelInfo, len(models))
		maps.Copy(modelsByProvider[providerName], models)
	}
	providerTypes := make(map[core.Provider]string, len(r.providerTypes))
	maps.Copy(providerTypes, r.providerTypes)
	modelListRaw := r.modelListRaw
	r.mu.RUnlock()

	if cacheBackend == nil {
		return nil
	}

	mc := &modelcache.ModelCache{
		UpdatedAt:     time.Now().UTC(),
		Providers:     make(map[string]modelcache.CachedProvider, len(modelsByProvider)),
		ModelListData: modelListRaw,
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
