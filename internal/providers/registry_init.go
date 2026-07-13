package providers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/modeldata"
)

// Initialize fetches models from all registered providers and populates the registry.
// This should be called on application startup.
func (r *ModelRegistry) Initialize(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	release, err := r.acquireRefresh(ctx)
	if err != nil {
		return err
	}
	defer release()
	return r.initialize(ctx)
}

func (r *ModelRegistry) initialize(ctx context.Context) error {
	providers, providerTypes, providerNames := r.snapshotProviders()
	configuredProviderModels, configuredProviderModelsMode := r.snapshotConfiguredProviderModels()

	fetched := r.fetchAllProviderModels(
		ctx,
		providers,
		providerTypes,
		providerNames,
		configuredProviderModels,
		configuredProviderModelsMode,
	)

	if fetched.totalModels == 0 {
		// Deliberately keep the previous inventory fresh (no swap, no stale
		// marking): when the whole sweep failed there is no healthy provider
		// left to route to, so marking everything stale would only turn
		// per-request 502/503s at the providers into 404s from an emptied
		// virtual-model resolution. It also covers control-plane-only
		// outages where /models is unreachable but inference still works.
		// Individual providers are still marked stale by the per-provider
		// recheck path as soon as a healthy alternative reappears.
		r.applyProviderRuntimeUpdates(fetched.runtimeUpdates)
		if fetched.failedProviders == len(providers) {
			return fmt.Errorf("failed to fetch models from any provider")
		}
		return fmt.Errorf("no models available: providers returned empty model lists")
	}

	r.applyFetchedInventory(providerTypes, fetched, len(providers))
	return nil
}

// snapshotProviders copies the registry's provider slice and type/name maps
// under a read lock so the rest of initialize can run without contending with
// readers.
func (r *ModelRegistry) snapshotProviders() ([]core.Provider, map[core.Provider]string, map[core.Provider]string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	providers := make([]core.Provider, len(r.providers))
	copy(providers, r.providers)
	providerTypes := make(map[core.Provider]string, len(r.providerTypes))
	providerNames := make(map[core.Provider]string, len(r.providerNames))
	maps.Copy(providerTypes, r.providerTypes)
	maps.Copy(providerNames, r.providerNames)
	return providers, providerTypes, providerNames
}

// fetchedInventory captures the result of one full provider fetch sweep.
// Shared by initial population and full refresh.
type fetchedInventory struct {
	models           map[string]*ModelInfo
	modelsByProvider map[string]map[string]*ModelInfo
	runtimeUpdates   map[string]providerRuntimeState
	totalModels      int
	failedProviders  int
}

// fetchAllProviderModels runs ListModels (or applies a configured allowlist)
// for every registered provider and aggregates the results. Network calls
// happen outside any registry lock so live readers keep serving the previous
// inventory.
func (r *ModelRegistry) fetchAllProviderModels(
	ctx context.Context,
	providers []core.Provider,
	providerTypes map[core.Provider]string,
	providerNames map[core.Provider]string,
	configuredProviderModels map[string][]string,
	configuredProviderModelsMode config.ConfiguredProviderModelsMode,
) fetchedInventory {
	out := fetchedInventory{
		models:           make(map[string]*ModelInfo),
		modelsByProvider: make(map[string]map[string]*ModelInfo),
		runtimeUpdates:   make(map[string]providerRuntimeState),
	}

	names := make([]string, len(providers))
	for i, provider := range providers {
		providerName := providerNames[provider]
		if providerName == "" {
			providerName = providerTypes[provider]
		}
		if providerName == "" {
			providerName = fmt.Sprintf("%p", provider)
		}
		names[i] = providerName
	}

	// Fetch every provider concurrently: the sweep shares one context budget
	// (30s on background refresh), so a sequential loop would let a single
	// slow upstream starve every provider after it — and a starved provider
	// is recorded as failed, dropping its models from the registry.
	// Aggregation below stays sequential in registration order so
	// first-provider-wins dedup remains deterministic.
	type fetchResult struct {
		resp             *core.ModelsResponse
		configuredReason configuredProviderModelsApplyReason
		fetchAt          time.Time
		err              error
	}
	results := make([]fetchResult, len(providers))
	var wg sync.WaitGroup
	for i, provider := range providers {
		wg.Add(1)
		go func(i int, provider core.Provider) {
			defer wg.Done()
			resp, configuredReason, fetchAt, err := fetchProviderInventory(
				ctx,
				provider,
				names[i],
				providerTypes[provider],
				configuredProviderModelsMode,
				configuredProviderModels[names[i]],
			)
			results[i] = fetchResult{resp: resp, configuredReason: configuredReason, fetchAt: fetchAt, err: err}
		}(i, provider)
	}
	wg.Wait()

	for i, provider := range providers {
		providerName := names[i]
		configuredModels := configuredProviderModels[providerName]
		resp := results[i].resp
		configuredReason := results[i].configuredReason
		fetchAt := results[i].fetchAt
		err := results[i].err
		var configuredUpstreamError string
		if configuredReason != configuredProviderModelsNotApplied {
			attrs := []any{
				"provider", providerName,
				"reason", string(configuredReason),
				"configured_models", len(configuredModels),
			}
			if err != nil {
				configuredUpstreamError = err.Error()
				attrs = append(attrs, "error", err)
				slog.Warn("upstream ListModels failed, using configured provider models", attrs...)
			} else if configuredReason == configuredProviderModelsAllowlist {
				slog.Debug("using configured provider models", attrs...)
			} else {
				slog.Warn("using configured provider models", attrs...)
			}
			err = nil
		}
		if err != nil {
			slog.Warn("failed to fetch models from provider",
				"provider", providerName,
				"error", err,
			)
			out.failedProviders++
			out.runtimeUpdates[providerName] = providerRuntimeState{
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
			out.failedProviders++
			out.runtimeUpdates[providerName] = providerRuntimeState{
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
			out.runtimeUpdates[providerName] = providerRuntimeState{
				registered:          true,
				lastModelFetchAt:    fetchAt,
				lastModelFetchError: err.Error(),
			}
			if _, ok := out.modelsByProvider[providerName]; !ok {
				out.modelsByProvider[providerName] = make(map[string]*ModelInfo)
			}
			continue
		}

		runtimeUpdate := providerRuntimeState{
			registered:          true,
			lastModelFetchAt:    fetchAt,
			lastModelFetchError: configuredUpstreamError,
		}
		// Mark the inventory as authoritatively populated when this fetch is the
		// last word on the provider's model list. That covers two cases:
		//  - upstream succeeded (no allowlist, or allowlist overlaid on a real
		//    response) — reason is configuredProviderModelsNotApplied
		//  - allowlist mode intentionally skipped upstream and produced the
		//    inventory from configuration — reason is configuredProviderModelsAllowlist
		// Fallback cases (configured*UpstreamError, *Nil, *Empty) keep
		// lastModelFetchSuccessAt unset so health surfaces "live refresh failed,
		// serving configured fallback".
		if configuredReason == configuredProviderModelsNotApplied ||
			configuredReason == configuredProviderModelsAllowlist {
			runtimeUpdate.lastModelFetchSuccessAt = fetchAt
		}
		if configuredReason == configuredProviderModelsNotApplied {
			runtimeUpdate.lastAvailabilityCheckAt = fetchAt
			runtimeUpdate.lastAvailabilityOKAt = fetchAt
		}
		out.runtimeUpdates[providerName] = runtimeUpdate

		if _, ok := out.modelsByProvider[providerName]; !ok {
			out.modelsByProvider[providerName] = make(map[string]*ModelInfo, len(resp.Data))
		}

		for _, model := range resp.Data {
			info := &ModelInfo{
				Model:        model,
				Provider:     provider,
				ProviderName: providerName,
				ProviderType: providerTypes[provider],
			}
			out.modelsByProvider[providerName][model.ID] = info

			if _, exists := out.models[model.ID]; exists {
				// First provider wins for unqualified lookups; later duplicates
				// stay reachable via modelsByProvider but lose the bare-id slot.
				slog.Debug("model already registered, skipping",
					"model", model.ID,
					"provider", providerName,
					"owner", model.OwnedBy,
				)
				continue
			}

			out.models[model.ID] = info
			out.totalModels++
		}
	}

	return out
}

// applyFetchedInventory enriches the freshly fetched maps with metadata,
// atomically swaps them onto the registry, marks initialized, and emits the
// summary log line.
//
// A provider that failed this sweep keeps its previous inventory, marked
// stale: direct requests still resolve (and fail at the provider with an
// honest 502/503 instead of "model not found"), while ModelAvailable reports
// false so virtual-model load balancing skips it — the same routing outcome
// the old wipe produced, without losing the inventory.
func (r *ModelRegistry) applyFetchedInventory(
	providerTypes map[core.Provider]string,
	fetched fetchedInventory,
	totalProviders int,
) {
	metadataStats := r.enrichFetchedProviderModelMaps(providerTypes, fetched.modelsByProvider)

	r.mu.Lock()
	stale := make(map[string]bool, len(fetched.runtimeUpdates))
	carriedForward := 0
	for name := range fetched.runtimeUpdates {
		if _, ok := fetched.modelsByProvider[name]; ok {
			continue // this sweep produced authoritative inventory
		}
		previous := r.modelsByProvider[name]
		if len(previous) == 0 {
			continue // nothing to carry forward
		}
		fetched.modelsByProvider[name] = previous
		stale[name] = true
		carriedForward++
	}
	r.modelsByProvider = fetched.modelsByProvider
	r.applyProviderRuntimeUpdatesLocked(fetched.runtimeUpdates)
	for name := range fetched.runtimeUpdates {
		state := r.providerRuntime[name]
		state.inventoryStale = stale[name]
		r.providerRuntime[name] = state
	}
	r.models = rebuildGlobalModelMap(r.modelsByProvider, r.freshFirstProviderOrderLocked())
	r.invalidateSortedCaches()
	r.mu.Unlock()

	r.initMu.Lock()
	r.initialized = true
	r.initMu.Unlock()

	attrs := []any{
		"total_models", fetched.totalModels,
		"providers", totalProviders,
		"failed_providers", fetched.failedProviders,
	}
	if carriedForward > 0 {
		attrs = append(attrs, "stale_inventory_providers", carriedForward)
	}
	attrs = append(attrs, metadataStats.slogAttrs()...)
	slog.Info("model registry initialized", attrs...)
}

func (r *ModelRegistry) enrichFetchedProviderModelMaps(
	providerTypes map[core.Provider]string,
	modelsByProvider map[string]map[string]*ModelInfo,
) metadataEnrichmentStats {
	r.mu.RLock()
	list := r.modelList
	r.mu.RUnlock()

	configOverrides := r.snapshotConfigOverrides()
	metadataStats := metadataEnrichmentStats{}
	if list != nil {
		metadataStats = enrichProviderModelMaps(list, providerTypes, modelsByProvider, nil)
	}
	metadataStats.Enriched += applyConfigMetadataOverrides(configOverrides, modelsByProvider, nil)
	return metadataStats
}

func fetchProviderInventory(
	ctx context.Context,
	provider core.Provider,
	providerName string,
	providerType string,
	mode config.ConfiguredProviderModelsMode,
	configuredModels []string,
) (*core.ModelsResponse, configuredProviderModelsApplyReason, time.Time, error) {
	fetchAt := time.Now().UTC()
	if mode == config.ConfiguredProviderModelsModeAllowlist && len(configuredModels) > 0 {
		resp, reason := applyConfiguredProviderModels(
			providerName,
			providerType,
			mode,
			configuredModels,
			nil,
			nil,
			fetchAt.Unix(),
		)
		return resp, reason, fetchAt, nil
	}

	resp, err := provider.ListModels(ctx)
	fetchAt = time.Now().UTC()
	resp, reason := applyConfiguredProviderModels(
		providerName,
		providerType,
		mode,
		configuredModels,
		resp,
		err,
		fetchAt.Unix(),
	)
	return resp, reason, fetchAt, err
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
			// A non-zero fetchAt represents a refresh attempt whose outcome
			// is captured authoritatively in lastModelFetchError (empty =
			// success, non-empty = failure). Overwrite unconditionally so an
			// old error doesn't survive a subsequent successful refresh —
			// this matters in particular for allowlist-mode refreshes which
			// don't bump SuccessAt but still produce usable models.
			current.lastModelFetchError = strings.TrimSpace(update.lastModelFetchError)
		}
		if !update.lastModelFetchSuccessAt.IsZero() {
			current.lastModelFetchSuccessAt = update.lastModelFetchSuccessAt
		}
		if !update.lastAvailabilityCheckAt.IsZero() {
			current.lastAvailabilityCheckAt = update.lastAvailabilityCheckAt
			current.lastAvailabilityError = strings.TrimSpace(update.lastAvailabilityError)
		}
		if !update.lastAvailabilityOKAt.IsZero() {
			current.lastAvailabilityOKAt = update.lastAvailabilityOKAt
			current.lastAvailabilityError = ""
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

// InitializeAsync starts model fetching in a background goroutine.
// It first loads any cached models for immediate availability, then refreshes from network.
// Returns immediately after loading cache. The background goroutine will update models
// and save to cache when network fetch completes.
func (r *ModelRegistry) InitializeAsync(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	// First, try to load from cache for instant startup
	cached, err := r.LoadFromCache(ctx)
	if err != nil {
		slog.Warn("failed to load models from cache", "error", err)
	} else if cached > 0 {
		slog.Info("serving traffic with cached models while refreshing", "cached_models", cached)
	}

	// Start background initialization. Derive the timeout from the caller's
	// ctx so shutdown cancellation propagates instead of leaving the goroutine
	// running until the 60s timeout fires on its own.
	go func() {
		initCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
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

// StartBackgroundRefresh starts a goroutine that periodically refreshes the model registry.
// If modelListURL is non-empty, the model list is also re-fetched on each tick.
// A positive recheckInterval additionally re-probes only the providers whose
// latest refresh failed, so outages and recoveries are detected without
// waiting for the next full refresh.
// The returned stop function is blocking: it cancels the refresh loop and waits
// for the goroutine to exit before returning, so callers should expect it to
// block during shutdown until any in-flight refresh work unwinds.
func (r *ModelRegistry) StartBackgroundRefresh(interval, recheckInterval time.Duration, modelListURL string) func() {
	if interval <= 0 {
		// time.NewTicker panics on non-positive durations and a refresh loop
		// with a zero interval would be meaningless. Skip the goroutine and
		// hand back a no-op stop so callers can still defer it safely.
		slog.Debug("model registry background refresh disabled", "interval", interval)
		return func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var stopOnce sync.Once

	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		var recheckCh <-chan time.Time
		if recheckInterval > 0 {
			recheckTicker := time.NewTicker(recheckInterval)
			defer recheckTicker.Stop()
			recheckCh = recheckTicker.C
		}

		for {
			select {
			case <-ctx.Done():
				return
			case <-recheckCh:
				r.recheckFailedProviders(ctx)
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

// recheckFailedProviders re-fetches only the providers whose latest refresh
// failed. Failures are logged at debug level — they are expected while a
// provider stays down; recovery is announced by the "provider models
// refreshed" log from the apply path.
func (r *ModelRegistry) recheckFailedProviders(ctx context.Context) {
	for _, providerName := range r.FailedProviderNames() {
		recheckCtx, recheckCancel := context.WithTimeout(ctx, 30*time.Second)
		_, err := r.RefreshProviderModels(recheckCtx, providerName)
		recheckCancel()
		if err == nil {
			continue
		}
		if isBenignBackgroundRefreshError(ctx, err) {
			return
		}
		slog.Debug("provider recheck still failing", "provider", providerName, "error", err)
	}
}

// RefreshModelList fetches the external model metadata list and re-enriches all
// currently registered models. It does not persist the model cache; callers that
// want durable startup data should call SaveToCache after this succeeds.
func (r *ModelRegistry) RefreshModelList(ctx context.Context, url string) (int, error) {
	if strings.TrimSpace(url) == "" {
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
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
