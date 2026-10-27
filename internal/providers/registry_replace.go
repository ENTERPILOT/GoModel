package providers

import (
	"strings"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
)

// ReplaceProviderWithConfig atomically replaces a named provider and its
// inventory settings, rebinding any still-eligible models to the new adapter.
func (r *ModelRegistry) ReplaceProviderWithConfig(provider core.Provider, cfg ProviderConfig) {
	providerName := strings.TrimSpace(cfg.Name)
	providerType := strings.TrimSpace(cfg.Type)
	if provider == nil || providerName == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	providers := make([]core.Provider, 0, len(r.providers)+1)
	inserted := false
	for _, current := range r.providers {
		if r.providerNames[current] != providerName {
			providers = append(providers, current)
			continue
		}
		if !inserted {
			providers = append(providers, provider)
			inserted = true
		}
		delete(r.providerTypes, current)
		delete(r.providerNames, current)
	}
	if !inserted {
		providers = append(providers, provider)
	}
	r.providers = providers
	r.providerTypes[provider] = providerType
	r.providerNames[provider] = providerName

	previous := r.discoveredByProvider[providerName]
	upstream := modelsResponseFromProviderMap(previous)
	configuredModels := normalizeConfiguredProviderModels(cfg.Models)
	response := upstream
	if len(previous) > 0 || config.ResolveConfiguredProviderModelsMode(r.configuredProviderModelsMode) != config.ConfiguredProviderModelsModeFallback {
		response, _ = applyConfiguredProviderModels(
			providerName,
			providerType,
			r.configuredProviderModelsMode,
			configuredModels,
			upstream,
			nil,
			0,
		)
	}

	retained := make(map[string]*ModelInfo, len(response.Data))
	for _, model := range response.Data {
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" {
			continue
		}
		if _, duplicate := retained[modelID]; duplicate {
			continue
		}
		if old := previous[modelID]; old != nil {
			copy := *old
			copy.Provider = provider
			copy.ProviderName = providerName
			copy.ProviderType = providerType
			retained[modelID] = &copy
			continue
		}
		retained[modelID] = newModelInfo(model, provider, providerName, providerType)
	}
	r.discoveredByProvider[providerName] = retained

	if len(configuredModels) == 0 {
		delete(r.configuredProviderModels, providerName)
	} else {
		if r.configuredProviderModels == nil {
			r.configuredProviderModels = make(map[string][]string)
		}
		r.configuredProviderModels[providerName] = configuredModels
	}
	if len(cfg.ModelMetadataOverrides) == 0 {
		delete(r.configMetadataOverrides, providerName)
	} else {
		if r.configMetadataOverrides == nil {
			r.configMetadataOverrides = make(map[string]map[string]*core.ModelMetadata)
		}
		overrides := make(map[string]*core.ModelMetadata, len(cfg.ModelMetadataOverrides))
		for id, metadata := range cfg.ModelMetadataOverrides {
			overrides[id] = metadata.Clone()
		}
		r.configMetadataOverrides[providerName] = overrides
	}
	if filter, ok := newModelFilter(cfg.ModelFilter); ok {
		if r.providerModelFilters == nil {
			r.providerModelFilters = make(map[string]modelFilter)
		}
		r.providerModelFilters[providerName] = filter
	} else {
		delete(r.providerModelFilters, providerName)
	}

	state := r.providerRuntime[providerName]
	state.registered = true
	r.providerRuntime[providerName] = state

	providerInventory := map[string]map[string]*ModelInfo{providerName: retained}
	enrichProviderModelMaps(r.modelList, r.providerTypes, providerInventory, nil)
	applyConfigMetadataOverrides(r.configMetadataOverrides, providerInventory, nil)
	applyInferredModelMetadata(providerInventory, nil)
	r.publishFilteredInventoryLocked()
	r.invalidateSortedCaches()
}
