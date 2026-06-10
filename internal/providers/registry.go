// Package providers provides model registry and routing for LLM providers.
package providers

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"gomodel/config"
	"gomodel/internal/cache/modelcache"
	"gomodel/internal/core"
	"gomodel/internal/modeldata"
)

// ModelInfo holds information about a model and its provider
type ModelInfo struct {
	Model        core.Model
	Provider     core.Provider
	ProviderName string
	ProviderType string
}

// ModelRegistry manages the mapping of models to their providers.
// It fetches models from providers on startup and caches them in memory.
// Supports loading from a cache (local file or Redis) for instant startup.
type ModelRegistry struct {
	mu               sync.RWMutex
	models           map[string]*ModelInfo            // model ID -> model info (first provider wins)
	modelsByProvider map[string]map[string]*ModelInfo // provider instance name -> model ID -> model info
	providers        []core.Provider
	providerTypes    map[core.Provider]string // provider -> type string
	providerNames    map[core.Provider]string // provider -> configured provider instance name
	providerRuntime  map[string]providerRuntimeState
	cache            modelcache.Cache     // cache backend (local or redis)
	initialized      bool                 // true when at least one successful network fetch completed
	initMu           sync.Mutex           // protects initialized flag
	refreshCh        chan struct{}        // serializes provider/model-list refresh cycles
	refreshOnce      sync.Once            // initializes refreshCh for zero-value safety
	modelList        *modeldata.ModelList // parsed model list (nil = not loaded)
	modelListRaw     json.RawMessage      // raw bytes for cache persistence
	// configMetadataOverrides holds operator-supplied metadata keyed by provider
	// instance name -> raw model ID. Applied after remote-registry enrichment as
	// a higher-priority layer. nil if no overrides declared.
	configMetadataOverrides map[string]map[string]*core.ModelMetadata
	// configuredProviderModels holds operator-supplied model inventories keyed by
	// configured provider instance name. The mode decides whether these entries
	// are fallback-only or an allowlist over the discovered upstream inventory.
	configuredProviderModels     map[string][]string
	configuredProviderModelsMode config.ConfiguredProviderModelsMode

	// Cached sorted slices, rebuilt lazily after models change.
	// nil means cache needs rebuilding. Protected by mu.
	sortedModels             []core.Model
	sortedModelsWithProvider []ModelWithProvider
	categoryCache            map[core.ModelCategory][]ModelWithProvider
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

// RegisterProviderWithNameAndType adds a provider with a configured provider instance name and type.
// Name is used for unambiguous provider/model selection (e.g. "provider/model") and cache persistence.
func (r *ModelRegistry) RegisterProviderWithNameAndType(provider core.Provider, providerName, providerType string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	providerName = strings.TrimSpace(providerName)
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

// GetProvider returns the provider for the given model, or nil if not found
func (r *ModelRegistry) GetProvider(model string) core.Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()

	providerName, modelID := splitModelSelector(model)
	if providerName != "" {
		if providerModels, ok := r.modelsByProvider[providerName]; ok {
			if info, exists := providerModelInfo(providerModels, modelID, model); exists {
				return info.Provider
			}
		}
		if r.hasConfiguredProviderNameLocked(providerName) {
			return nil
		}
		// Fall through: the slash may be part of the model ID (e.g. "meta-llama/Meta-Llama-3-70B")
	}

	if info, ok := r.models[model]; ok {
		return info.Provider
	}
	return nil
}

// GetModel returns the registry-backed model info for the given model, or nil if not found.
// Callers must treat the returned data as read-only.
func (r *ModelRegistry) GetModel(model string) *ModelInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	providerName, modelID := splitModelSelector(model)
	if providerName != "" {
		if providerModels, ok := r.modelsByProvider[providerName]; ok {
			if info, exists := providerModelInfo(providerModels, modelID, model); exists {
				return info
			}
		}
		if r.hasConfiguredProviderNameLocked(providerName) {
			return nil
		}
		// Fall through: the slash may be part of the model ID
	}

	if info, ok := r.models[model]; ok {
		return info
	}
	return nil
}

// LookupModel returns a shallow copy of the concrete model for the given selector.
// Qualified selectors use the configured provider name prefix when present.
func (r *ModelRegistry) LookupModel(model string) (*core.Model, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	providerName, modelID := splitModelSelector(model)
	if providerName != "" {
		if providerModels, ok := r.modelsByProvider[providerName]; ok {
			if info, exists := providerModelInfo(providerModels, modelID, model); exists {
				cloned := info.Model
				return &cloned, true
			}
		}
		if r.hasConfiguredProviderNameLocked(providerName) {
			return nil, false
		}
		// Fall through: the slash may be part of the model ID
	}

	if info, ok := r.models[model]; ok {
		cloned := info.Model
		return &cloned, true
	}
	return nil, false
}

// Supports returns true if the registry has a provider for the given model
func (r *ModelRegistry) Supports(model string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	providerName, modelID := splitModelSelector(model)
	if providerName != "" {
		if providerModels, ok := r.modelsByProvider[providerName]; ok {
			if _, exists := providerModelInfo(providerModels, modelID, model); exists {
				return true
			}
		}
		if r.hasConfiguredProviderNameLocked(providerName) {
			return false
		}
		// Fall through: the slash may be part of the model ID
	}

	_, ok := r.models[model]
	return ok
}

// ListModels returns all models in the registry, sorted by model ID for consistent ordering.
// The sorted slice is cached and rebuilt only when the underlying models change.
// Returns a defensive copy so callers cannot mutate the internal cache.
func (r *ModelRegistry) ListModels() []core.Model {
	r.mu.RLock()
	if cached := r.sortedModels; cached != nil {
		r.mu.RUnlock()
		return append([]core.Model(nil), cached...)
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	// Double-check: another goroutine may have built it while we waited for the lock.
	if r.sortedModels != nil {
		return append([]core.Model(nil), r.sortedModels...)
	}

	models := make([]core.Model, 0, len(r.models))
	for _, info := range r.models {
		models = append(models, info.Model)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })

	r.sortedModels = models
	return append([]core.Model(nil), models...)
}

// ListPublicModels returns all provider-backed models as public selectors in
// providerName/modelID form, sorted by public model ID. Models the owning
// provider cannot actually serve (audio-only models on providers without audio
// support) are not advertised.
func (r *ModelRegistry) ListPublicModels() []core.Model {
	r.mu.RLock()
	defer r.mu.RUnlock()

	total := 0
	for _, models := range r.modelsByProvider {
		total += len(models)
	}

	result := make([]core.Model, 0, total)
	for providerName, models := range r.modelsByProvider {
		for modelID, info := range models {
			if !providerCanServeModel(info) {
				continue
			}
			model := info.Model
			model.ID = qualifyPublicModelID(providerName, modelID)
			model.OwnedBy = providerName
			result = append(result, model)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

// providerCanServeModel reports whether the owning provider implements the
// capabilities a model needs. Upstream inventories and the metadata registry
// can list audio-only models (TTS/STT) for providers whose gateway adapter has
// no audio support; advertising those invites calls that can only fail with
// "does not support audio operations". Models without mode metadata are kept —
// missing data must not hide a model.
func providerCanServeModel(info *ModelInfo) bool {
	if !isAudioOnlyModel(info.Model) {
		return true
	}
	_, ok := info.Provider.(core.AudioProvider)
	return ok
}

// isAudioOnlyModel reports whether every declared mode is an audio operation.
func isAudioOnlyModel(model core.Model) bool {
	if model.Metadata == nil || len(model.Metadata.Modes) == 0 {
		return false
	}
	for _, mode := range model.Metadata.Modes {
		if mode != "audio_speech" && mode != "audio_transcription" {
			return false
		}
	}
	return true
}

// ModelCount returns the number of registered models
func (r *ModelRegistry) ModelCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.models)
}

// GetProviderType returns the provider type string for the given model.
// Returns empty string if the model is not found.
func (r *ModelRegistry) GetProviderType(model string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	providerName, modelID := splitModelSelector(model)
	if providerName != "" {
		if providerModels, ok := r.modelsByProvider[providerName]; ok {
			if info, exists := providerModelInfo(providerModels, modelID, model); exists {
				return info.ProviderType
			}
		}
		if r.hasConfiguredProviderNameLocked(providerName) {
			return ""
		}
		// Fall through: the slash may be part of the model ID
	}

	if info, ok := r.models[model]; ok {
		return info.ProviderType
	}
	return ""
}

// GetProviderName returns the concrete configured provider instance name for
// the given model selector. Returns empty string if the model is not found.
func (r *ModelRegistry) GetProviderName(model string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	providerName, modelID := splitModelSelector(model)
	if providerName != "" {
		if providerModels, ok := r.modelsByProvider[providerName]; ok {
			if info, exists := providerModelInfo(providerModels, modelID, model); exists {
				return strings.TrimSpace(info.ProviderName)
			}
		}
		if r.hasConfiguredProviderNameLocked(providerName) {
			return ""
		}
	}

	if info, ok := r.models[model]; ok {
		return strings.TrimSpace(info.ProviderName)
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
		if strings.TrimSpace(r.providerTypes[provider]) != providerType {
			continue
		}
		return strings.TrimSpace(r.providerNames[provider])
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
		if strings.TrimSpace(r.providerNames[provider]) != providerName {
			continue
		}
		return strings.TrimSpace(r.providerTypes[provider])
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
		if strings.TrimSpace(r.providerNames[provider]) == providerName {
			return provider
		}
	}
	return nil
}

// ProviderTypes returns the unique registered provider types in sorted order.
// This inventory is independent of discovered models.
func (r *ModelRegistry) ProviderTypes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	seen := make(map[string]struct{}, len(r.providerTypes))
	result := make([]string, 0, len(r.providerTypes))
	for _, provider := range r.providers {
		providerType := strings.TrimSpace(r.providerTypes[provider])
		if providerType == "" {
			continue
		}
		if _, exists := seen[providerType]; exists {
			continue
		}
		seen[providerType] = struct{}{}
		result = append(result, providerType)
	}
	sort.Strings(result)
	return result
}

// ProviderNames returns the configured provider instance names in registration order.
func (r *ModelRegistry) ProviderNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]string, 0, len(r.providers))
	for _, provider := range r.providers {
		providerName := strings.TrimSpace(r.providerNames[provider])
		if providerName == "" {
			continue
		}
		result = append(result, providerName)
	}
	return result
}

func splitModelSelector(model string) (providerName, modelID string) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", ""
	}
	parts := strings.SplitN(model, "/", 2)
	if len(parts) != 2 {
		return "", model
	}
	providerName = strings.TrimSpace(parts[0])
	modelID = strings.TrimSpace(parts[1])
	if providerName == "" || modelID == "" {
		return "", model
	}
	return providerName, modelID
}

func providerModelInfo(providerModels map[string]*ModelInfo, modelID, rawModel string) (*ModelInfo, bool) {
	modelID = strings.TrimSpace(modelID)
	rawModel = strings.TrimSpace(rawModel)
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

func qualifyPublicModelID(providerName, modelID string) string {
	providerName = strings.TrimSpace(providerName)
	modelID = strings.TrimSpace(modelID)
	if providerName == "" {
		return modelID
	}
	if modelID == "" {
		return providerName
	}
	return providerName + "/" + modelID
}

func (r *ModelRegistry) hasConfiguredProviderNameLocked(providerName string) bool {
	providerName = strings.TrimSpace(providerName)
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

// ModelWithProvider holds a model alongside provider metadata and its public selector.
type ModelWithProvider struct {
	Model        core.Model `json:"model"`
	ProviderType string     `json:"provider_type"`
	ProviderName string     `json:"provider_name"`
	Selector     string     `json:"selector"`
}

// ListModelsWithProvider returns all provider-backed models with provider metadata,
// sorted by public selector.
// The sorted slice is cached and rebuilt only when the underlying models change.
// Returns a defensive copy so callers cannot mutate the internal cache.
func (r *ModelRegistry) ListModelsWithProvider() []ModelWithProvider {
	r.mu.RLock()
	if cached := r.sortedModelsWithProvider; cached != nil {
		r.mu.RUnlock()
		return append([]ModelWithProvider(nil), cached...)
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sortedModelsWithProvider != nil {
		return append([]ModelWithProvider(nil), r.sortedModelsWithProvider...)
	}

	total := 0
	for _, providerModels := range r.modelsByProvider {
		total += len(providerModels)
	}

	result := make([]ModelWithProvider, 0, total)
	for providerName, providerModels := range r.modelsByProvider {
		for modelID, info := range providerModels {
			publicProviderName := providerName
			if info.ProviderName != "" {
				publicProviderName = info.ProviderName
			}
			result = append(result, ModelWithProvider{
				Model:        info.Model,
				ProviderType: info.ProviderType,
				ProviderName: publicProviderName,
				Selector:     qualifyPublicModelID(publicProviderName, modelID),
			})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Selector < result[j].Selector })

	r.sortedModelsWithProvider = result
	return append([]ModelWithProvider(nil), result...)
}

// cacheableCategory reports whether category is a known value that should be cached.
// CategoryAll is handled separately (delegates to ListModelsWithProvider).
var cacheableCategories = map[core.ModelCategory]struct{}{
	core.CategoryTextGeneration: {},
	core.CategoryEmbedding:      {},
	core.CategoryImage:          {},
	core.CategoryAudio:          {},
	core.CategoryVideo:          {},
	core.CategoryUtility:        {},
}

// ListModelsWithProviderByCategory returns provider-backed models filtered by
// category, sorted by public selector.
// If category is CategoryAll, returns all models (same as ListModelsWithProvider).
// Results for known categories are cached and rebuilt only when the underlying models change.
// Returns a defensive copy so callers cannot mutate the internal cache.
func (r *ModelRegistry) ListModelsWithProviderByCategory(category core.ModelCategory) []ModelWithProvider {
	if category == core.CategoryAll {
		return r.ListModelsWithProvider()
	}

	_, cacheable := cacheableCategories[category]

	if cacheable {
		r.mu.RLock()
		if r.categoryCache != nil {
			if cached, ok := r.categoryCache[category]; ok {
				r.mu.RUnlock()
				return append([]ModelWithProvider(nil), cached...)
			}
		}
		r.mu.RUnlock()
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if cacheable && r.categoryCache != nil {
		if cached, ok := r.categoryCache[category]; ok {
			return append([]ModelWithProvider(nil), cached...)
		}
	}

	result := make([]ModelWithProvider, 0)
	for _, providerModels := range r.modelsByProvider {
		for modelID, info := range providerModels {
			if info.Model.Metadata == nil || !hasCategory(info.Model.Metadata.Categories, category) {
				continue
			}
			result = append(result, ModelWithProvider{
				Model:        info.Model,
				ProviderType: info.ProviderType,
				ProviderName: info.ProviderName,
				Selector:     qualifyPublicModelID(info.ProviderName, modelID),
			})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Selector < result[j].Selector })

	if cacheable {
		if r.categoryCache == nil {
			r.categoryCache = make(map[core.ModelCategory][]ModelWithProvider)
		}
		r.categoryCache[category] = result
	}
	return result
}

// hasCategory returns true if the category slice contains the target category.
func hasCategory(cats []core.ModelCategory, target core.ModelCategory) bool {
	return slices.Contains(cats, target)
}

// CategoryCount holds a model category and the number of models in it.
type CategoryCount struct {
	Category    core.ModelCategory `json:"category"`
	DisplayName string             `json:"display_name"`
	Count       int                `json:"count"`
}

// categoryDisplayNames maps categories to human-readable display names.
var categoryDisplayNames = map[core.ModelCategory]string{
	core.CategoryAll:            "All",
	core.CategoryTextGeneration: "Text Generation",
	core.CategoryEmbedding:      "Embeddings",
	core.CategoryImage:          "Image",
	core.CategoryAudio:          "Audio",
	core.CategoryVideo:          "Video",
	core.CategoryUtility:        "Utility",
}

// GetCategoryCounts returns model counts per category, in display order.
// A model with multiple categories is counted in each.
func (r *ModelRegistry) GetCategoryCounts() []CategoryCount {
	r.mu.RLock()
	defer r.mu.RUnlock()

	counts := make(map[core.ModelCategory]int)
	total := 0
	for _, providerModels := range r.modelsByProvider {
		for _, info := range providerModels {
			total++
			if info.Model.Metadata != nil {
				for _, cat := range info.Model.Metadata.Categories {
					counts[cat]++
				}
			}
		}
	}

	allCategories := core.AllCategories()
	result := make([]CategoryCount, 0, len(allCategories))
	for _, cat := range allCategories {
		count := counts[cat]
		if cat == core.CategoryAll {
			count = total
		}
		displayName := categoryDisplayNames[cat]
		if displayName == "" {
			displayName = string(cat)
		}
		result = append(result, CategoryCount{
			Category:    cat,
			DisplayName: displayName,
			Count:       count,
		})
	}
	return result
}

// ProviderCount returns the number of registered providers
func (r *ModelRegistry) ProviderCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.providers)
}

// RecordAvailabilityCheck stores the latest startup or explicit availability
// probe result for a configured provider name.
func (r *ModelRegistry) RecordAvailabilityCheck(providerName string, err error) {
	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.providerRuntime[providerName]
	state.registered = true
	state.lastAvailabilityCheckAt = time.Now().UTC()
	if err != nil {
		state.lastAvailabilityError = err.Error()
	} else {
		state.lastAvailabilityOKAt = state.lastAvailabilityCheckAt
		state.lastAvailabilityError = ""
	}
	r.providerRuntime[providerName] = state
}

// ProviderRuntimeSnapshots returns runtime diagnostics for configured providers
// keyed by configured provider name.
func (r *ModelRegistry) ProviderRuntimeSnapshots() []ProviderRuntimeSnapshot {
	r.mu.RLock()
	result := make([]ProviderRuntimeSnapshot, 0, len(r.providers))
	for _, provider := range r.providers {
		providerName := strings.TrimSpace(r.providerNames[provider])
		if providerName == "" {
			continue
		}
		state := r.providerRuntime[providerName]
		result = append(result, ProviderRuntimeSnapshot{
			Name:                    providerName,
			Type:                    strings.TrimSpace(r.providerTypes[provider]),
			Registered:              state.registered,
			DiscoveredModelCount:    len(r.modelsByProvider[providerName]),
			LastModelFetchAt:        timePtrUTC(state.lastModelFetchAt),
			LastModelFetchSuccessAt: timePtrUTC(state.lastModelFetchSuccessAt),
			LastModelFetchError:     strings.TrimSpace(state.lastModelFetchError),
			LastAvailabilityCheckAt: timePtrUTC(state.lastAvailabilityCheckAt),
			LastAvailabilityOKAt:    timePtrUTC(state.lastAvailabilityOKAt),
			LastAvailabilityError:   strings.TrimSpace(state.lastAvailabilityError),
		})
	}
	r.mu.RUnlock()

	initialized := r.IsInitialized()
	for i := range result {
		result[i].RegistryInitialized = initialized
		result[i].UsingCachedModels = result[i].DiscoveredModelCount > 0 &&
			!initialized &&
			result[i].LastModelFetchSuccessAt == nil
	}

	return result
}
