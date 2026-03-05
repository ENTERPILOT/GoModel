package providers

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"time"

	"gomodel/config"
	"gomodel/internal/cache"
	"gomodel/internal/core"
	"gomodel/internal/modeldata"
)

// InitResult holds the initialized provider infrastructure and cleanup functions.
type InitResult struct {
	Registry *ModelRegistry
	Router   *Router
	Cache    cache.Cache
	Factory  *ProviderFactory

	// stopRefresh is called to stop the background refresh goroutine
	stopRefresh func()
}

// Close releases all resources and stops background goroutines.
// Safe to call multiple times (but stopRefresh is only called once).
func (r *InitResult) Close() error {
	if r.stopRefresh != nil {
		r.stopRefresh()
		r.stopRefresh = nil // Prevent double-call
	}
	if r.Cache != nil {
		return r.Cache.Close()
	}
	return nil
}

// Init initializes the provider registry, cache, and router.
//
// It performs:
// 1. Provider config resolution (env var overlay, filtering, resilience merging)
// 2. Cache initialization (local or Redis based on config)
// 3. Provider instantiation and registration
// 4. Async model loading (from cache first, then network refresh)
// 5. Best-effort background model-list fetch (goroutine with ~45s timeout that
//    calls modeldata.Fetch, registry.EnrichModels, and SaveToCache)
// 6. Background refresh scheduling (interval from cfg.Cache.RefreshInterval)
// 7. Router creation
//
// The caller must call InitResult.Close() during shutdown.
func Init(ctx context.Context, result *config.LoadResult, factory *ProviderFactory) (*InitResult, error) {
	if result == nil {
		return nil, fmt.Errorf("load result is required")
	}
	if factory == nil {
		return nil, fmt.Errorf("factory is required")
	}

	providerMap := resolveProviders(result.RawProviders, result.Config.Resilience)

	modelCache, err := initCache(result.Config)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize cache: %w", err)
	}

	registry := NewModelRegistry()
	registry.SetCache(modelCache)

	count, err := initializeProviders(providerMap, factory, registry)
	if err != nil {
		modelCache.Close()
		return nil, err
	}
	if count == 0 {
		modelCache.Close()
		return nil, fmt.Errorf("no providers were successfully initialized")
	}

	slog.Info("starting non-blocking model registry initialization...")
	registry.InitializeAsync(ctx)

	slog.Info("model registry configured",
		"cached_models", registry.ModelCount(),
		"providers", registry.ProviderCount(),
	)

	// Fetch model list in background (best-effort, non-blocking)
	modelListURL := result.Config.Cache.Model.ModelList.URL
	if modelListURL != "" {
		go func() {
			fetchCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			defer cancel()

			list, raw, err := modeldata.Fetch(fetchCtx, modelListURL)
			if err != nil {
				slog.Warn("failed to fetch model list", "url", modelListURL, "error", err)
				return
			}
			if list == nil {
				return
			}

			registry.SetModelList(list, raw)
			registry.EnrichModels()

			if err := registry.SaveToCache(fetchCtx); err != nil {
				slog.Warn("failed to save cache after model list fetch", "error", err)
			}
			slog.Info("model list loaded",
				"models", len(list.Models),
				"providers", len(list.Providers),
				"provider_models", len(list.ProviderModels),
			)
		}()
	}

	refreshInterval := time.Duration(result.Config.Cache.Model.RefreshInterval) * time.Second
	if refreshInterval <= 0 {
		refreshInterval = time.Hour
	}
	stopRefresh := registry.StartBackgroundRefresh(refreshInterval, modelListURL)

	router, err := NewRouter(registry)
	if err != nil {
		stopRefresh()
		modelCache.Close()
		return nil, fmt.Errorf("failed to create router: %w", err)
	}

	return &InitResult{
		Registry:    registry,
		Router:      router,
		Cache:       modelCache,
		Factory:     factory,
		stopRefresh: stopRefresh,
	}, nil
}

// initCache initializes the appropriate cache backend based on configuration.
func initCache(cfg *config.Config) (cache.Cache, error) {
	m := cfg.Cache.Model
	if m.Redis != nil && m.Redis.URL != "" {
		ttl := time.Duration(m.Redis.TTL) * time.Second
		if ttl == 0 {
			ttl = cache.DefaultRedisTTL
		}
		redisCfg := cache.RedisModelCacheConfig{
			URL: m.Redis.URL,
			Key: m.Redis.Key,
			TTL: ttl,
		}
		modelCache, err := cache.NewRedisModelCache(redisCfg)
		if err != nil {
			return nil, err
		}
		key := m.Redis.Key
		if key == "" {
			key = cache.DefaultRedisKey
		}
		slog.Info("using redis cache", "url", m.Redis.URL, "key", key)
		return modelCache, nil
	}
	if m.Local != nil {
		cacheDir := m.Local.CacheDir
		if cacheDir == "" {
			cacheDir = ".cache"
		}
		cacheFile := filepath.Join(cacheDir, "models.json")
		slog.Info("using local file cache", "path", cacheFile)
		return cache.NewLocalCache(cacheFile), nil
	}
	return nil, fmt.Errorf("cache.model: must have either local or redis configured")
}

// initializeProviders instantiates and registers all resolved providers.
// Returns the count of successfully initialized providers.
func initializeProviders(providerMap map[string]ProviderConfig, factory *ProviderFactory, registry *ModelRegistry) (int, error) {
	// Sort provider names for deterministic initialization order
	names := make([]string, 0, len(providerMap))
	for name := range providerMap {
		names = append(names, name)
	}
	sort.Strings(names)

	var count int
	for _, name := range names {
		pCfg := providerMap[name]
		p, err := factory.Create(pCfg)
		if err != nil {
			slog.Error("failed to initialize provider",
				"name", name,
				"type", pCfg.Type,
				"error", err)
			continue
		}

		// Check availability for providers that support it
		if checker, ok := p.(core.AvailabilityChecker); ok {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := checker.CheckAvailability(ctx); err != nil {
				slog.Warn("provider not available, skipping",
					"name", name,
					"type", pCfg.Type,
					"reason", err.Error())
				cancel()
				continue
			}
			cancel()
		}

		registry.RegisterProviderWithNameAndType(p, name, pCfg.Type)
		count++
		slog.Info("provider initialized", "name", name, "type", pCfg.Type)
	}

	return count, nil
}
