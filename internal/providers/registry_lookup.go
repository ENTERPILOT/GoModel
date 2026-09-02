package providers

import (
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// ResolveProviderSelector resolves a qualified "<segment>/<modelID>" selector,
// where segment is a provider instance name or a provider type, to the concrete
// provider-name-qualified selector. Provider-name matches take precedence over
// provider-type matches, mirroring catalog-scan resolution. Returns ok=false
// when the segment+model pair is not a direct name/type match so callers can
// fall back to slower resolution for raw slash-shaped IDs and other edge cases.
//
// This is O(1) and exists so the per-request routing path does not copy and
// linearly scan the entire model catalog.
func (r *ModelRegistry) ResolveProviderSelector(segment, modelID string) (core.ModelSelector, bool) {
	segment = strings.TrimSpace(segment)
	modelID = strings.TrimSpace(modelID)
	if segment == "" || modelID == "" {
		return core.ModelSelector{}, false
	}
	key := segment + "/" + modelID

	r.mu.RLock()
	if r.qualifiedByName != nil {
		sel, ok := lookupSelectorIndex(r.qualifiedByName, r.qualifiedByType, key)
		r.mu.RUnlock()
		return sel, ok
	}
	r.mu.RUnlock()

	r.mu.Lock()
	r.buildSelectorIndexLocked()
	sel, ok := lookupSelectorIndex(r.qualifiedByName, r.qualifiedByType, key)
	r.mu.Unlock()
	return sel, ok
}

func lookupSelectorIndex(byName, byType map[string]core.ModelSelector, key string) (core.ModelSelector, bool) {
	if sel, ok := byName[key]; ok {
		return sel, true
	}
	if sel, ok := byType[key]; ok {
		return sel, true
	}
	return core.ModelSelector{}, false
}

// buildSelectorIndexLocked populates the qualified selector index from the
// current catalog. Caller must hold the write lock. On provider-type collisions
// it keeps the lexicographically smallest provider name so resolution is
// deterministic and matches the previous sorted-scan behavior.
func (r *ModelRegistry) buildSelectorIndexLocked() {
	if r.qualifiedByName != nil {
		return
	}
	total := 0
	for _, providerModels := range r.modelsByProvider {
		total += len(providerModels)
	}
	byName := make(map[string]core.ModelSelector, total)
	byType := make(map[string]core.ModelSelector, total)
	for providerName, providerModels := range r.modelsByProvider {
		for _, info := range providerModels {
			publicName := providerName
			if info.ProviderName != "" {
				publicName = info.ProviderName
			}
			id := info.Model.ID
			if publicName == "" || id == "" {
				continue
			}
			sel := core.ModelSelector{Provider: publicName, Model: id}
			byName[publicName+"/"+id] = sel
			if info.ProviderType != "" {
				typeKey := info.ProviderType + "/" + id
				if existing, ok := byType[typeKey]; !ok || sel.Provider < existing.Provider {
					byType[typeKey] = sel
				}
			}
		}
	}
	r.qualifiedByName = byName
	r.qualifiedByType = byType
}

// lookupLocked resolves a model selector to its registry entry. Qualified
// selectors ("<provider>/<model>") are looked up under the configured provider
// name first; a qualified selector naming a configured provider that does not
// serve the model is a definitive miss. Otherwise the slash may be part of the
// model ID itself (e.g. "meta-llama/Meta-Llama-3-70B"), so the raw selector is
// tried against the global map. Callers must hold r.mu.
func (r *ModelRegistry) lookupLocked(model string) (*ModelInfo, bool) {
	model = strings.TrimSpace(model)
	providerName, modelID := splitModelSelector(model)
	if providerName != "" {
		if providerModels, ok := r.modelsByProvider[providerName]; ok {
			if info, exists := providerModelInfo(providerModels, modelID, model); exists {
				return info, true
			}
		}
		if r.hasConfiguredProviderNameLocked(providerName) {
			return nil, false
		}
	}

	info, ok := r.models[model]
	return info, ok
}

// GetProvider returns the provider for the given model, or nil if not found
func (r *ModelRegistry) GetProvider(model string) core.Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if info, ok := r.lookupLocked(model); ok {
		return info.Provider
	}
	return nil
}

// GetModel returns the registry-backed model info for the given model, or nil if not found.
// Callers must treat the returned data as read-only.
func (r *ModelRegistry) GetModel(model string) *ModelInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if info, ok := r.lookupLocked(model); ok {
		return info
	}
	return nil
}

// LookupModel returns a shallow copy of the concrete model for the given selector.
// Qualified selectors use the configured provider name prefix when present.
func (r *ModelRegistry) LookupModel(model string) (*core.Model, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if info, ok := r.lookupLocked(model); ok {
		cloned := info.Model
		return &cloned, true
	}
	return nil, false
}

// Supports returns true if the registry has a provider for the given model
func (r *ModelRegistry) Supports(model string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	_, ok := r.lookupLocked(model)
	return ok
}

// ModelAvailable reports whether the model is registered AND its provider's
// inventory is fresh (latest refresh succeeded). Virtual-model load balancing
// uses this to skip providers whose upstream is failing, while Supports keeps
// resolving stale models so direct requests still reach the provider and fail
// with an honest 502/503 instead of "model not found".
func (r *ModelRegistry) ModelAvailable(model string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	info, ok := r.lookupLocked(model)
	if !ok {
		return false
	}
	return !r.providerRuntime[info.ProviderName].inventoryStale
}

// GetProviderType returns the provider type string for the given model.
// Returns empty string if the model is not found.
func (r *ModelRegistry) GetProviderType(model string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if info, ok := r.lookupLocked(model); ok {
		return info.ProviderType
	}
	return ""
}

// GetProviderName returns the concrete configured provider instance name for
// the given model selector. Returns empty string if the model is not found.
func (r *ModelRegistry) GetProviderName(model string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if info, ok := r.lookupLocked(model); ok {
		return info.ProviderName
	}
	return ""
}

// GetProviderNameForType returns the first registered configured provider name
// for the given provider type. This follows the same first-registered behavior
// used when provider-typed routes resolve a concrete provider instance.
func (r *ModelRegistry) GetProviderNameForType(providerType string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	providerType = strings.TrimSpace(providerType)
	if providerType == "" {
		return ""
	}
	for _, provider := range r.providers {
		if r.providerTypes[provider] != providerType {
			continue
		}
		return r.providerNames[provider]
	}
	return ""
}

// GetProviderTypeForName returns the provider type for the given concrete
// configured provider instance name.
func (r *ModelRegistry) GetProviderTypeForName(providerName string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		return ""
	}
	for _, provider := range r.providers {
		if r.providerNames[provider] != providerName {
			continue
		}
		return r.providerTypes[provider]
	}
	return ""
}

// ProviderByType returns the first registered provider for the given provider type.
// This lookup is independent of discovered models so provider-typed routes keep
// working even when a provider currently exposes zero models.
func (r *ModelRegistry) ProviderByType(providerType string) core.Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()

	providerType = strings.TrimSpace(providerType)
	if providerType == "" {
		return nil
	}
	for _, provider := range r.providers {
		if r.providerTypes[provider] == providerType {
			return provider
		}
	}
	return nil
}

// ProviderByName returns the registered provider for a configured provider
// instance name.
func (r *ModelRegistry) ProviderByName(providerName string) core.Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()

	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		return nil
	}
	for _, provider := range r.providers {
		if r.providerNames[provider] == providerName {
			return provider
		}
	}
	return nil
}

// splitModelSelector splits an already-trimmed selector into its provider and
// model segments. Whitespace around the slash is tolerated.
func splitModelSelector(model string) (providerName, modelID string) {
	if model == "" {
		return "", ""
	}
	before, after, found := strings.Cut(model, "/")
	if !found {
		return "", model
	}
	providerName = strings.TrimSpace(before)
	modelID = strings.TrimSpace(after)
	if providerName == "" || modelID == "" {
		return "", model
	}
	return providerName, modelID
}

func providerModelInfo(providerModels map[string]*ModelInfo, modelID, rawModel string) (*ModelInfo, bool) {
	if info, exists := providerModels[modelID]; exists {
		return info, true
	}
	if rawModel != "" && rawModel != modelID {
		if info, exists := providerModels[rawModel]; exists {
			return info, true
		}
	}
	return nil, false
}

func (r *ModelRegistry) hasConfiguredProviderNameLocked(providerName string) bool {
	if providerName == "" {
		return false
	}
	for _, configuredName := range r.providerNames {
		if configuredName == providerName {
			return true
		}
	}
	return false
}
