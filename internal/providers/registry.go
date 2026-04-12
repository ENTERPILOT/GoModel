// Package providers provides model registry and routing for LLM providers.
package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

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
		models:           make(map[string]*ModelInfo),
		modelsByProvider: make(map[string]map[string]*ModelInfo),
		providerTypes:    make(map[core.Provider]string),
		providerNames:    make(map[core.Provider]string),
		providerRuntime:  make(map[string]providerRuntimeState),
		refreshCh:        make(chan struct{}, 1),
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

// Initialize fetches models from all registered providers and populates the registry.
// This should be called on application startup.
func (r *ModelRegistry) Initialize(ctx context.Context) error {
	release, err := r.acquireRefresh(ctx)
	if err != nil {
		return err
	}
	defer release()
	return r.initialize(ctx)
}

func (r *ModelRegistry) initialize(ctx context.Context) error {
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
	runtimeUpdates := make(map[string]providerRuntimeState)

	r.mu.RLock()
	providerTypes := make(map[core.Provider]string, len(r.providerTypes))
	providerNames := make(map[core.Provider]string, len(r.providerNames))
	maps.Copy(providerTypes, r.providerTypes)
	maps.Copy(providerNames, r.providerNames)
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
		fetchAt := time.Now().UTC()
		if err != nil {
			slog.Warn("failed to fetch models from provider",
				"provider", providerName,
				"error", err,
			)
			failedProviders++
			runtimeUpdates[providerName] = providerRuntimeState{
				registered:          true,
				lastModelFetchAt:    fetchAt,
				lastModelFetchError: err.Error(),
			}
			continue
		}

		if resp == nil {
			err := errors.New("provider returned nil model list")
			slog.Warn("failed to fetch models from provider",
				"provider", providerName,
				"error", err,
			)
			failedProviders++
			runtimeUpdates[providerName] = providerRuntimeState{
				registered:          true,
				lastModelFetchAt:    fetchAt,
				lastModelFetchError: err.Error(),
			}
			continue
		}

		if len(resp.Data) == 0 {
			err := errors.New("provider returned empty model list")
			slog.Warn("provider returned empty model list",
				"provider", providerName,
			)
			runtimeUpdates[providerName] = providerRuntimeState{
				registered:          true,
				lastModelFetchAt:    fetchAt,
				lastModelFetchError: err.Error(),
			}
			if _, ok := newModelsByProvider[providerName]; !ok {
				newModelsByProvider[providerName] = make(map[string]*ModelInfo)
			}
			continue
		}

		runtimeUpdates[providerName] = providerRuntimeState{
			registered:              true,
			lastModelFetchAt:        fetchAt,
			lastModelFetchSuccessAt: fetchAt,
		}

		if _, ok := newModelsByProvider[providerName]; !ok {
			newModelsByProvider[providerName] = make(map[string]*ModelInfo, len(resp.Data))
		}

		for _, model := range resp.Data {
			info := &ModelInfo{
				Model:        model,
				Provider:     provider,
				ProviderName: providerName,
				ProviderType: providerTypes[provider],
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
		r.applyProviderRuntimeUpdates(runtimeUpdates)
		if failedProviders == len(providers) {
			return fmt.Errorf("failed to fetch models from any provider")
		}
		return fmt.Errorf("no models available: providers returned empty model lists")
	}

	// Enrich models with metadata from the model list (if loaded)
	r.mu.RLock()
	list := r.modelList
	r.mu.RUnlock()
	metadataStats := metadataEnrichmentStats{}
	if list != nil {
		metadataStats = enrichProviderModelMaps(list, providerTypes, newModelsByProvider, nil)
	}

	// Atomically swap the models map and invalidate sorted caches
	r.mu.Lock()
	r.models = newModels
	r.modelsByProvider = newModelsByProvider
	r.applyProviderRuntimeUpdatesLocked(runtimeUpdates)
	r.invalidateSortedCaches()
	r.mu.Unlock()

	// Mark as initialized
	r.initMu.Lock()
	r.initialized = true
	r.initMu.Unlock()

	attrs := []any{
		"total_models", totalModels,
		"providers", len(providers),
		"failed_providers", failedProviders,
	}
	attrs = append(attrs, metadataStats.slogAttrs()...)
	slog.Info("model registry initialized", attrs...)

	return nil
}

func (r *ModelRegistry) applyProviderRuntimeUpdates(updates map[string]providerRuntimeState) {
	if len(updates) == 0 {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.applyProviderRuntimeUpdatesLocked(updates)
}

func (r *ModelRegistry) applyProviderRuntimeUpdatesLocked(updates map[string]providerRuntimeState) {
	for providerName, update := range updates {
		current := r.providerRuntime[providerName]
		current.registered = update.registered || current.registered
		if !update.lastModelFetchAt.IsZero() {
			current.lastModelFetchAt = update.lastModelFetchAt
		}
		if !update.lastModelFetchSuccessAt.IsZero() {
			current.lastModelFetchSuccessAt = update.lastModelFetchSuccessAt
			current.lastModelFetchError = ""
		} else if strings.TrimSpace(update.lastModelFetchError) != "" {
			current.lastModelFetchError = update.lastModelFetchError
		}
		r.providerRuntime[providerName] = current
	}
}

// Refresh updates the model registry by fetching fresh model lists from providers.
// This can be called periodically to keep the registry up to date.
func (r *ModelRegistry) Refresh(ctx context.Context) error {
	return r.Initialize(ctx)
}

func (r *ModelRegistry) acquireRefresh(ctx context.Context) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, registryRefreshAcquireError(err)
	}
	ch := r.refreshSemaphore()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, registryRefreshAcquireError(ctx.Err())
	}
}

func (r *ModelRegistry) refreshSemaphore() chan struct{} {
	r.refreshOnce.Do(func() {
		if r.refreshCh == nil {
			r.refreshCh = make(chan struct{}, 1)
		}
	})
	return r.refreshCh
}

func registryRefreshAcquireError(err error) *core.GatewayError {
	if errors.Is(err, context.DeadlineExceeded) {
		return core.NewProviderError("model_registry", http.StatusGatewayTimeout, "model registry refresh timed out before start", err)
	}
	return core.NewProviderError("model_registry", http.StatusRequestTimeout, "model registry refresh canceled before start", err)
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
	nameToProviderType := make(map[string]string, len(r.providerNames))
	for provider, pName := range r.providerNames {
		nameToProvider[pName] = provider
		nameToProviderType[pName] = r.providerTypes[provider]
	}
	r.mu.RUnlock()

	// Populate model maps from grouped cache structure. Unqualified lookups keep "first provider wins".
	newModels := make(map[string]*ModelInfo)
	newModelsByProvider := make(map[string]map[string]*ModelInfo)
	for providerName, cachedProv := range modelCache.Providers {
		provider, ok := nameToProvider[providerName]
		if !ok {
			// Provider not configured, skip all its models
			continue
		}
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
			if info, exists := providerModels[modelID]; exists {
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
			if info, exists := providerModels[modelID]; exists {
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
			if _, exists := providerModels[modelID]; exists {
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
// providerName/modelID form, sorted by public model ID.
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
			model := info.Model
			model.ID = qualifyPublicModelID(providerName, modelID)
			model.OwnedBy = providerName
			result = append(result, model)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
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
			if info, exists := providerModels[modelID]; exists {
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
	if r.modelList == nil || len(r.models) == 0 {
		return metadataEnrichmentStats{}
	}

	providerTypes := make(map[core.Provider]string, len(r.providerTypes))
	maps.Copy(providerTypes, r.providerTypes)

	replacements := make(map[*ModelInfo]*ModelInfo, len(r.models))
	stats := enrichProviderModelMaps(r.modelList, providerTypes, r.modelsByProvider, replacements)
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
	maps.Copy(m, r.providerTypes)
	return m
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

// StartBackgroundRefresh starts a goroutine that periodically refreshes the model registry.
// If modelListURL is non-empty, the model list is also re-fetched on each tick.
// The returned stop function is blocking: it cancels the refresh loop and waits
// for the goroutine to exit before returning, so callers should expect it to
// block during shutdown until any in-flight refresh work unwinds.
func (r *ModelRegistry) StartBackgroundRefresh(interval time.Duration, modelListURL string) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var stopOnce sync.Once

	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				refreshCtx, refreshCancel := context.WithTimeout(ctx, 30*time.Second)
				err := r.Initialize(refreshCtx)
				refreshCancel()
				if err != nil {
					if !isBenignBackgroundRefreshError(ctx, err) {
						slog.Warn("background model refresh failed", "error", err)
					}
				} else {
					func() {
						cacheCtx, cacheCancel := context.WithTimeout(ctx, 10*time.Second)
						defer cacheCancel()
						if err := r.SaveToCache(cacheCtx); err != nil {
							if !isBenignBackgroundRefreshError(ctx, err) {
								slog.Warn("failed to save models to cache after refresh", "error", err)
							}
						}
					}()
				}

				// Also refresh model list if configured
				if modelListURL != "" {
					r.refreshModelList(ctx, modelListURL)
				}
			}
		}
	}()

	return func() {
		stopOnce.Do(func() {
			cancel()
			<-done
		})
	}
}

// RefreshModelList fetches the external model metadata list and re-enriches all
// currently registered models. It does not persist the model cache; callers that
// want durable startup data should call SaveToCache after this succeeds.
func (r *ModelRegistry) RefreshModelList(ctx context.Context, url string) (int, error) {
	if strings.TrimSpace(url) == "" {
		return 0, nil
	}

	release, err := r.acquireRefresh(ctx)
	if err != nil {
		return 0, err
	}
	defer release()

	models, _, err := r.refreshModelListLocked(ctx, url)
	return models, err
}

func (r *ModelRegistry) refreshModelListLocked(ctx context.Context, url string) (int, metadataEnrichmentStats, error) {
	list, raw, err := modeldata.Fetch(ctx, url)
	if err != nil {
		return 0, metadataEnrichmentStats{}, err
	}
	if list == nil {
		return 0, metadataEnrichmentStats{}, nil
	}

	metadataStats := r.setModelListAndEnrich(list, raw)
	return len(list.Models), metadataStats, nil
}

// refreshModelList fetches the model list and re-enriches all models.
func (r *ModelRegistry) refreshModelList(ctx context.Context, url string) {
	fetchCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	release, err := r.acquireRefresh(fetchCtx)
	if err != nil {
		if !isBenignBackgroundRefreshError(ctx, err) {
			slog.Warn("failed to acquire model list refresh", "url", url, "error", err)
		}
		return
	}
	var (
		models        int
		metadataStats metadataEnrichmentStats
	)
	func() {
		defer release()
		models, metadataStats, err = r.refreshModelListLocked(fetchCtx, url)
	}()
	if err != nil {
		if !isBenignBackgroundRefreshError(ctx, err) {
			slog.Warn("failed to refresh model list", "url", url, "error", err)
		}
		return
	}
	if models == 0 {
		return
	}

	if err := r.SaveToCache(fetchCtx); err != nil {
		if !isBenignBackgroundRefreshError(ctx, err) {
			slog.Warn("failed to save cache after model list refresh", "error", err)
		}
	}
	attrs := []any{"models", models}
	attrs = append(attrs, metadataStats.slogAttrs()...)
	slog.Debug("model list refreshed", attrs...)
}

func isBenignBackgroundRefreshError(parent context.Context, err error) bool {
	if err == nil {
		return true
	}
	if parent == nil || parent.Err() == nil {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
