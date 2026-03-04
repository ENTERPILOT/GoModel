// Package providers provides model registry and routing for LLM providers.
package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"gomodel/internal/cache"
	"gomodel/internal/core"
	"gomodel/internal/modeldata"
)

// ModelInfo holds information about a model and its provider
type ModelInfo struct {
	Model    core.Model
	Provider core.Provider
}

// ModelRegistry manages the mapping of models to their providers.
// It fetches models from providers on startup and caches them in memory.
// Supports loading from a cache (local file or Redis) for instant startup.
type ModelRegistry struct {
	mu               sync.RWMutex
	models           map[string]*ModelInfo             // model ID -> model info (first provider wins)
	modelsByProvider map[string]map[string]*ModelInfo  // provider instance name -> model ID -> model info
	providers        []core.Provider
	providerTypes    map[core.Provider]string // provider -> type string
	providerNames    map[core.Provider]string // provider -> configured provider instance name
	cache            cache.Cache              // cache backend (local or redis)
	initialized      bool                     // true when at least one successful network fetch completed
	initMu           sync.Mutex               // protects initialized flag
	modelList        *modeldata.ModelList     // parsed model list (nil = not loaded)
	modelListRaw     json.RawMessage          // raw bytes for cache persistence

	// Cached sorted slices, rebuilt lazily after models change.
	// nil means cache needs rebuilding. Protected by mu.
	sortedModels             []core.Model
	sortedModelsWithProvider []ModelWithProvider
	categoryCache            map[core.ModelCategory][]ModelWithProvider
}

// NewModelRegistry creates a new model registry
func NewModelRegistry() *ModelRegistry {
	return &ModelRegistry{
		models:           make(map[string]*ModelInfo),
		modelsByProvider: make(map[string]map[string]*ModelInfo),
		providerTypes:    make(map[core.Provider]string),
		providerNames:    make(map[core.Provider]string),
	}
}

// SetCache sets the cache backend for persistent model storage.
// The cache can be a local file-based cache or a Redis cache.
func (r *ModelRegistry) SetCache(c cache.Cache) {
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
}

// Initialize fetches models from all registered providers and populates the registry.
// This should be called on application startup.
func (r *ModelRegistry) Initialize(ctx context.Context) error {
	// Get a snapshot of providers with a read lock
	r.mu.RLock()
	providers := make([]core.Provider, len(r.providers))
	copy(providers, r.providers)
	r.mu.RUnlock()

	// Build new model maps without holding the lock.
	// This allows concurrent reads to continue using the existing map
	// while we fetch models from providers (which may involve network calls).
	newModels := make(map[string]*ModelInfo)
	newModelsByProvider := make(map[string]map[string]*ModelInfo)
	var totalModels int
	var failedProviders int

	r.mu.RLock()
	providerTypes := make(map[core.Provider]string, len(r.providerTypes))
	providerNames := make(map[core.Provider]string, len(r.providerNames))
	for p, t := range r.providerTypes {
		providerTypes[p] = t
	}
	for p, n := range r.providerNames {
		providerNames[p] = n
	}
	r.mu.RUnlock()

	for _, provider := range providers {
		providerName := providerNames[provider]
		if providerName == "" {
			providerName = providerTypes[provider]
		}
		if providerName == "" {
			providerName = fmt.Sprintf("%p", provider)
		}

		resp, err := provider.ListModels(ctx)
		if err != nil {
			slog.Warn("failed to fetch models from provider",
				"provider", providerName,
				"error", err,
			)
			failedProviders++
			continue
		}

		if _, ok := newModelsByProvider[providerName]; !ok {
			newModelsByProvider[providerName] = make(map[string]*ModelInfo, len(resp.Data))
		}

		for _, model := range resp.Data {
			info := &ModelInfo{
				Model:    model,
				Provider: provider,
			}
			newModelsByProvider[providerName][model.ID] = info

			if _, exists := newModels[model.ID]; exists {
				// Model already registered by another provider, skip
				// First provider wins for unqualified lookups.
				slog.Debug("model already registered, skipping",
					"model", model.ID,
					"provider", providerName,
					"owner", model.OwnedBy,
				)
				continue
			}

			newModels[model.ID] = info
			totalModels++
		}
	}

	if totalModels == 0 {
		if failedProviders == len(providers) {
			return fmt.Errorf("failed to fetch models from any provider")
		}
		return fmt.Errorf("no models available: providers returned empty model lists")
	}

	// Enrich models with metadata from the model list (if loaded)
	r.mu.RLock()
	list := r.modelList
	r.mu.RUnlock()
	if list != nil {
		accessor := &registryAccessor{models: newModels, providerTypes: r.snapshotProviderTypes()}
		modeldata.Enrich(accessor, list)
	}

	// Atomically swap the models map and invalidate sorted caches
	r.mu.Lock()
	r.models = newModels
	r.modelsByProvider = newModelsByProvider
	r.invalidateSortedCaches()
	r.mu.Unlock()

	// Mark as initialized
	r.initMu.Lock()
	r.initialized = true
	r.initMu.Unlock()

	slog.Info("model registry initialized",
		"total_models", totalModels,
		"providers", len(providers),
		"failed_providers", failedProviders,
	)

	return nil
}

// Refresh updates the model registry by fetching fresh model lists from providers.
// This can be called periodically to keep the registry up to date.
func (r *ModelRegistry) Refresh(ctx context.Context) error {
	return r.Initialize(ctx)
}

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
	for provider, pName := range r.providerNames {
		nameToProvider[pName] = provider
	}
	r.mu.RUnlock()

	// Populate model maps from cache rows. Unqualified lookups keep "first provider wins".
	newModels := make(map[string]*ModelInfo, len(modelCache.Models))
	newModelsByProvider := make(map[string]map[string]*ModelInfo)
	for _, cached := range modelCache.Models {
		provider, ok := nameToProvider[cached.Provider]
		if !ok {
			// Provider not configured, skip this model
			continue
		}
		info := &ModelInfo{
			Model: core.Model{
				ID:      cached.ModelID,
				Object:  cached.Object,
				OwnedBy: cached.OwnedBy,
				Created: cached.Created,
			},
			Provider: provider,
		}
		if _, ok := newModelsByProvider[cached.Provider]; !ok {
			newModelsByProvider[cached.Provider] = make(map[string]*ModelInfo)
		}
		newModelsByProvider[cached.Provider][cached.ModelID] = info
		if _, exists := newModels[cached.ModelID]; !exists {
			newModels[cached.ModelID] = info
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
	if list != nil {
		accessor := &registryAccessor{models: newModels, providerTypes: r.snapshotProviderTypes()}
		modeldata.Enrich(accessor, list)
	}

	r.mu.Lock()
	r.models = newModels
	r.modelsByProvider = newModelsByProvider
	r.invalidateSortedCaches()
	if list != nil {
		r.modelList = list
		r.modelListRaw = modelCache.ModelListData
	}
	r.mu.Unlock()

	slog.Info("loaded models from cache",
		"models", len(newModels),
		"cache_updated_at", modelCache.UpdatedAt,
	)

	return len(newModels), nil
}

// SaveToCache saves the current model list to the cache backend.
func (r *ModelRegistry) SaveToCache(ctx context.Context) error {
	r.mu.RLock()
	cacheBackend := r.cache
	modelsByProvider := make(map[string]map[string]*ModelInfo, len(r.modelsByProvider))
	for providerName, models := range r.modelsByProvider {
		modelsByProvider[providerName] = make(map[string]*ModelInfo, len(models))
		for modelID, info := range models {
			modelsByProvider[providerName][modelID] = info
		}
	}
	providerTypes := make(map[core.Provider]string, len(r.providerTypes))
	for k, v := range r.providerTypes {
		providerTypes[k] = v
	}
	modelListRaw := r.modelListRaw
	r.mu.RUnlock()

	if cacheBackend == nil {
		return nil
	}

	// Build cache structure as a slice of provider/model rows.
	modelCache := &cache.ModelCache{
		UpdatedAt:     time.Now().UTC(),
		Models:        make([]cache.CachedModel, 0),
		ModelListData: modelListRaw,
	}

	providerNames := make([]string, 0, len(modelsByProvider))
	for providerName := range modelsByProvider {
		providerNames = append(providerNames, providerName)
	}
	sort.Strings(providerNames)

	for _, providerName := range providerNames {
		modelIDs := make([]string, 0, len(modelsByProvider[providerName]))
		for modelID := range modelsByProvider[providerName] {
			modelIDs = append(modelIDs, modelID)
		}
		sort.Strings(modelIDs)

		for _, modelID := range modelIDs {
			info := modelsByProvider[providerName][modelID]
			pType, ok := providerTypes[info.Provider]
			if !ok {
				// Skip models without a known provider type.
				continue
			}
			modelCache.Models = append(modelCache.Models, cache.CachedModel{
				ModelID:      modelID,
				Provider:     providerName,
				ProviderType: pType,
				Object:       info.Model.Object,
				OwnedBy:      info.Model.OwnedBy,
				Created:      info.Model.Created,
			})
		}
	}

	if err := cacheBackend.Set(ctx, modelCache); err != nil {
		return fmt.Errorf("failed to save cache: %w", err)
	}

	slog.Debug("saved models to cache", "models", len(modelCache.Models))
	return nil
}

// InitializeAsync starts model fetching in a background goroutine.
// It first loads any cached models for immediate availability, then refreshes from network.
// Returns immediately after loading cache. The background goroutine will update models
// and save to cache when network fetch completes.
func (r *ModelRegistry) InitializeAsync(ctx context.Context) {
	// First, try to load from cache for instant startup
	cached, err := r.LoadFromCache(ctx)
	if err != nil {
		slog.Warn("failed to load models from cache", "error", err)
	} else if cached > 0 {
		slog.Info("serving traffic with cached models while refreshing", "cached_models", cached)
	}

	// Start background initialization
	go func() {
		initCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		if err := r.Initialize(initCtx); err != nil {
			slog.Warn("background model initialization failed", "error", err)
			return
		}

		// Save to cache for next startup
		if err := r.SaveToCache(initCtx); err != nil {
			slog.Warn("failed to save models to cache", "error", err)
		}
	}()
}

// IsInitialized returns true if at least one successful network fetch has completed.
// This can be used to check if the registry has fresh data or is only serving from cache.
func (r *ModelRegistry) IsInitialized() bool {
	r.initMu.Lock()
	defer r.initMu.Unlock()
	return r.initialized
}

// GetProvider returns the provider for the given model, or nil if not found
func (r *ModelRegistry) GetProvider(model string) core.Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()

	providerName, modelID := splitModelSelector(model)
	if providerName != "" {
		if providerModels, ok := r.modelsByProvider[providerName]; ok {
			if info, exists := providerModels[modelID]; exists {
				return info.Provider
			}
		}
		return nil
	}

	if info, ok := r.models[modelID]; ok {
		return info.Provider
	}
	return nil
}

// GetModel returns the model info for the given model, or nil if not found
func (r *ModelRegistry) GetModel(model string) *ModelInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	providerName, modelID := splitModelSelector(model)
	if providerName != "" {
		if providerModels, ok := r.modelsByProvider[providerName]; ok {
			return providerModels[modelID]
		}
		return nil
	}

	if info, ok := r.models[modelID]; ok {
		return info
	}
	return nil
}

// Supports returns true if the registry has a provider for the given model
func (r *ModelRegistry) Supports(model string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	providerName, modelID := splitModelSelector(model)
	if providerName != "" {
		providerModels, ok := r.modelsByProvider[providerName]
		if !ok {
			return false
		}
		_, ok = providerModels[modelID]
		return ok
	}

	_, ok := r.models[modelID]
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
			if info, exists := providerModels[modelID]; exists {
				return r.providerTypes[info.Provider]
			}
		}
		return ""
	}

	info, ok := r.models[modelID]
	if ok {
		return r.providerTypes[info.Provider]
	}

	return ""
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

// ModelWithProvider holds a model alongside its provider type string.
type ModelWithProvider struct {
	Model        core.Model `json:"model"`
	ProviderType string     `json:"provider_type"`
}

// ListModelsWithProvider returns all models with their provider types, sorted by model ID.
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

	result := make([]ModelWithProvider, 0, len(r.models))
	for _, info := range r.models {
		result = append(result, ModelWithProvider{
			Model:        info.Model,
			ProviderType: r.providerTypes[info.Provider],
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Model.ID < result[j].Model.ID })

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

// ListModelsWithProviderByCategory returns models filtered by category, sorted by model ID.
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
	for _, info := range r.models {
		if info.Model.Metadata == nil || !hasCategory(info.Model.Metadata.Categories, category) {
			continue
		}
		result = append(result, ModelWithProvider{
			Model:        info.Model,
			ProviderType: r.providerTypes[info.Provider],
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Model.ID < result[j].Model.ID })

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
	for _, c := range cats {
		if c == target {
			return true
		}
	}
	return false
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
	for _, info := range r.models {
		if info.Model.Metadata != nil {
			for _, cat := range info.Model.Metadata.Categories {
				counts[cat]++
			}
		}
	}

	allCategories := core.AllCategories()
	result := make([]CategoryCount, 0, len(allCategories))
	for _, cat := range allCategories {
		count := counts[cat]
		if cat == core.CategoryAll {
			count = len(r.models)
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

// SetModelList stores the parsed model list and its raw bytes for cache persistence.
func (r *ModelRegistry) SetModelList(list *modeldata.ModelList, raw json.RawMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.modelList = list
	r.modelListRaw = raw
}

// EnrichModels re-applies model list metadata to all currently registered models.
// Call this after SetModelList to update existing models with the new metadata.
// Holds the write lock for the entire operation to prevent races with concurrent
// readers (e.g. ListModels) that may read Model.Metadata.
func (r *ModelRegistry) EnrichModels() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.modelList == nil || len(r.models) == 0 {
		return
	}

	providerTypes := make(map[core.Provider]string, len(r.providerTypes))
	for k, v := range r.providerTypes {
		providerTypes[k] = v
	}

	accessor := &registryAccessor{models: r.models, providerTypes: providerTypes}
	modeldata.Enrich(accessor, r.modelList)
	r.invalidateSortedCaches()
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
	meta := r.GetModelMetadata(model)
	if meta != nil && meta.Pricing != nil {
		return meta.Pricing
	}
	if providerType != "" {
		meta = r.ResolveMetadata(providerType, model)
		if meta != nil && meta.Pricing != nil {
			return meta.Pricing
		}
	}
	return nil
}

// snapshotProviderTypes returns a copy of the providerTypes map for use outside the lock.
func (r *ModelRegistry) snapshotProviderTypes() map[core.Provider]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m := make(map[core.Provider]string, len(r.providerTypes))
	for k, v := range r.providerTypes {
		m[k] = v
	}
	return m
}

// registryAccessor implements modeldata.ModelInfoAccessor.
// The models map may be either a snapshot (Initialize, LoadFromCache) or the live
// registry map (EnrichModels, which holds the write lock for the entire operation).
type registryAccessor struct {
	models        map[string]*ModelInfo
	providerTypes map[core.Provider]string
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
	return a.providerTypes[info.Provider]
}

func (a *registryAccessor) SetMetadata(modelID string, meta *core.ModelMetadata) {
	if info, ok := a.models[modelID]; ok {
		info.Model.Metadata = meta
	}
}

// StartBackgroundRefresh starts a goroutine that periodically refreshes the model registry.
// If modelListURL is non-empty, the model list is also re-fetched on each tick.
// Returns a cancel function to stop the refresh loop.
func (r *ModelRegistry) StartBackgroundRefresh(interval time.Duration, modelListURL string) func() {
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				refreshCtx, refreshCancel := context.WithTimeout(context.Background(), 30*time.Second)
				if err := r.Refresh(refreshCtx); err != nil {
					slog.Warn("background model refresh failed", "error", err)
				} else {
					// Save to cache after successful refresh
					if err := r.SaveToCache(refreshCtx); err != nil {
						slog.Warn("failed to save models to cache after refresh", "error", err)
					}
				}
				refreshCancel()

				// Also refresh model list if configured
				if modelListURL != "" {
					r.refreshModelList(modelListURL)
				}
			}
		}
	}()

	return cancel
}

// refreshModelList fetches the model list and re-enriches all models.
func (r *ModelRegistry) refreshModelList(url string) {
	fetchCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	list, raw, err := modeldata.Fetch(fetchCtx, url)
	if err != nil {
		slog.Warn("failed to refresh model list", "url", url, "error", err)
		return
	}
	if list == nil {
		return
	}

	r.SetModelList(list, raw)
	r.EnrichModels()

	if err := r.SaveToCache(fetchCtx); err != nil {
		slog.Warn("failed to save cache after model list refresh", "error", err)
	}
	slog.Debug("model list refreshed", "models", len(list.Models))
}
