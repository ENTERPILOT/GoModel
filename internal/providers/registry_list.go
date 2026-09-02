package providers

import (
	"slices"
	"sort"

	"github.com/enterpilot/gomodel/internal/core"
)

// providerAdvertisedLocked reports whether providerName's models belong in
// model listings. A provider whose latest refresh or availability probe
// failed (inventoryStale) keeps its carried-forward inventory resolvable for
// direct requests, but offline providers must not advertise models in
// GET /v1/models, the dashboard, or failover candidate selection.
// Caller must hold r.mu.
func (r *ModelRegistry) providerAdvertisedLocked(providerName string) bool {
	return !r.providerRuntime[providerName].inventoryStale
}

// ListModels returns all advertised models in the registry, sorted by model ID
// for consistent ordering. Models whose owning provider's inventory is stale
// are excluded (see providerAdvertisedLocked).
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
		if !r.providerAdvertisedLocked(info.ProviderName) {
			continue
		}
		models = append(models, info.Model)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })

	r.sortedModels = models
	return append([]core.Model(nil), models...)
}

// ListPublicModels returns all provider-backed models as public selectors in
// providerName/modelID form, sorted by public model ID. Models the owning
// provider cannot actually serve (audio-only models on providers without audio
// support) and models from providers with stale inventory are not advertised.
func (r *ModelRegistry) ListPublicModels() []core.Model {
	r.mu.RLock()
	defer r.mu.RUnlock()

	total := 0
	for _, models := range r.modelsByProvider {
		total += len(models)
	}

	result := make([]core.Model, 0, total)
	for providerName, models := range r.modelsByProvider {
		if !r.providerAdvertisedLocked(providerName) {
			continue
		}
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

// ListUnqualifiedPublicModels returns advertised models under their bare
// model IDs, sorted by ID. When several providers expose the same model ID,
// only the provider an unqualified request resolves to (the global catalog
// entry, see rebuildGlobalModelMap) is listed, so the response never
// advertises an ID that would route somewhere else. OwnedBy carries the
// provider name so callers can still tell which provider serves the model.
func (r *ModelRegistry) ListUnqualifiedPublicModels() []core.Model {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]core.Model, 0, len(r.models))
	for modelID, info := range r.models {
		if !r.providerAdvertisedLocked(info.ProviderName) || !providerCanServeModel(info) {
			continue
		}
		model := info.Model
		model.ID = modelID
		model.OwnedBy = info.ProviderName
		result = append(result, model)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

// providerCanServeModel reports whether the owning provider implements the
// capabilities a model needs. Upstream inventories and the metadata registry
// can list audio-only (TTS/STT) or image-only models for providers whose
// gateway adapter has no audio or image support; advertising those invites
// calls that can only fail with "does not support audio operations" or "does
// not support image generation". Models without mode metadata are kept —
// missing data must not hide a model.
func providerCanServeModel(info *ModelInfo) bool {
	if isAudioOnlyModel(info.Model) {
		_, ok := info.Provider.(core.AudioProvider)
		return ok
	}
	if isImageOnlyModel(info.Model) {
		_, ok := info.Provider.(core.ImageProvider)
		return ok
	}
	return true
}

// isAudioOnlyModel reports whether every declared mode is an audio operation.
func isAudioOnlyModel(model core.Model) bool {
	return onlyHasModes(model, "audio_speech", "audio_transcription")
}

// isImageOnlyModel reports whether every declared mode is an image operation.
func isImageOnlyModel(model core.Model) bool {
	return onlyHasModes(model, "image_generation", "image_edit")
}

func onlyHasModes(model core.Model, allowed ...string) bool {
	if model.Metadata == nil || len(model.Metadata.Modes) == 0 {
		return false
	}
	for _, mode := range model.Metadata.Modes {
		if !slices.Contains(allowed, mode) {
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

// ProviderTypes returns the unique registered provider types in sorted order.
// This inventory is independent of discovered models.
func (r *ModelRegistry) ProviderTypes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	seen := make(map[string]struct{}, len(r.providerTypes))
	result := make([]string, 0, len(r.providerTypes))
	for _, provider := range r.providers {
		providerType := r.providerTypes[provider]
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
		providerName := r.providerNames[provider]
		if providerName == "" {
			continue
		}
		result = append(result, providerName)
	}
	return result
}

func qualifyPublicModelID(providerName, modelID string) string {
	if providerName == "" {
		return modelID
	}
	if modelID == "" {
		return providerName
	}
	return providerName + "/" + modelID
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
		if !r.providerAdvertisedLocked(providerName) {
			continue
		}
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
	for providerName, providerModels := range r.modelsByProvider {
		if !r.providerAdvertisedLocked(providerName) {
			continue
		}
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
	for providerName, providerModels := range r.modelsByProvider {
		if !r.providerAdvertisedLocked(providerName) {
			continue
		}
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
