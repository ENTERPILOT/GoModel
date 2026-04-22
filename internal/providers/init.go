package providers

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"gomodel/config"
	"gomodel/internal/cache"
	"gomodel/internal/cache/modelcache"
	"gomodel/internal/core"
	"gomodel/internal/modeldata"
)

// InitResult holds the initialized provider infrastructure and cleanup functions.
type InitResult struct {
	Registry *ModelRegistry
	Router   *Router
	Cache    modelcache.Cache
	Factory  *ProviderFactory

	// ConfiguredProviders is the effective, admin-safe provider inventory keyed
	// by configured provider name.
	ConfiguredProviders []SanitizedProviderConfig

	// CredentialResolvedProviders is the env-merged, credential-filtered providers
	// map (same keys as Router). Keys match top-level providers YAML names.
	CredentialResolvedProviders map[string]config.RawProviderConfig

	// stopRefresh is called to stop the background refresh goroutine
	stopRefresh func()

	closeOnce sync.Once
	closeErr  error
}

// Close releases all resources and stops background goroutines.
// Safe to call multiple times (but stopRefresh is only called once).
func (r *InitResult) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		if r.stopRefresh != nil {
			r.stopRefresh()
			r.stopRefresh = nil
		}
		if r.Cache != nil {
			r.closeErr = r.Cache.Close()
		}
	})
	return r.closeErr
}

// Init initializes the provider registry, cache, and router.
//
// It performs:
//  1. Provider config resolution (env var overlay, filtering, resilience merging)
//  2. Cache initialization (local or Redis based on config)
//  3. Provider instantiation and registration
//  4. Async model loading (from cache first, then network refresh)
//  5. Best-effort background model-list fetch (goroutine with ~45s timeout that
//     calls modeldata.Fetch, registry.EnrichModels, and SaveToCache)
//  6. Background refresh scheduling (interval from cfg.Cache.RefreshInterval)
//  7. Router creation
//
// The caller must call InitResult.Close() during shutdown.
func Init(ctx context.Context, result *config.LoadResult, factory *ProviderFactory) (*InitResult, error) {
	if result == nil {
		return nil, fmt.Errorf("load result is required")
	}
	if factory == nil {
		return nil, fmt.Errorf("factory is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	providerMap, credentialResolved := resolveProviders(result.RawProviders, result.Config.Resilience, factory.discoveryConfigsSnapshot())

	modelCache, err := initCache(result.Config)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize cache: %w", err)
	}

	registry := NewModelRegistry()
	registry.SetCache(modelCache)

	count, err := initializeProviders(ctx, providerMap, factory, registry)
	if err != nil {
		modelCache.Close()
		return nil, err
	}
	if count == 0 {
		modelCache.Close()
		return nil, fmt.Errorf("no providers were successfully registered")
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
			metadataStats := registry.enrichModels()

			if err := registry.SaveToCache(fetchCtx); err != nil {
				slog.Warn("failed to save cache after model list fetch", "error", err)
			}
			attrs := []any{
				"models", len(list.Models),
				"providers", len(list.Providers),
				"provider_models", len(list.ProviderModels),
			}
			attrs = append(attrs, metadataStats.slogAttrs()...)
			slog.Info("model list loaded", attrs...)
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
		ConfiguredProviders:         SanitizeProviderConfigs(providerMap),
		Registry:                    registry,
		Router:                      router,
		Cache:                       modelCache,
		Factory:                     factory,
		CredentialResolvedProviders: credentialResolved,
		stopRefresh:                 stopRefresh,
	}, nil
}

// initCache initializes the appropriate cache backend based on configuration.
func initCache(cfg *config.Config) (modelcache.Cache, error) {
	m := cfg.Cache.Model
	if m.Redis != nil && m.Redis.URL != "" {
		ttl := time.Duration(m.Redis.TTL) * time.Second
		if ttl == 0 {
			ttl = cache.DefaultRedisTTL
		}
		redisCfg := modelcache.RedisModelCacheConfig{
			URL: m.Redis.URL,
			Key: m.Redis.Key,
			TTL: ttl,
		}
		mc, err := modelcache.NewRedisModelCache(redisCfg)
		if err != nil {
			return nil, err
		}
		key := m.Redis.Key
		if key == "" {
			key = modelcache.DefaultRedisKey
		}
		slog.Info("using redis cache", "key", key)
		return mc, nil
	}
	if m.Local != nil {
		cacheDir := m.Local.CacheDir
		if cacheDir == "" {
			cacheDir = ".cache"
		}
		cacheFile := filepath.Join(cacheDir, "models.json")
		slog.Info("using local file cache", "path", cacheFile)
		return modelcache.NewLocalCache(cacheFile), nil
	}
	return nil, fmt.Errorf("cache.model: must have either local or redis configured")
}

// initializeProviders instantiates and registers all resolved providers.
// Returns the count of successfully registered providers.
func initializeProviders(ctx context.Context, providerMap map[string]ProviderConfig, factory *ProviderFactory, registry *ModelRegistry) (int, error) {
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

		// Availability checks are diagnostics only. Providers stay registered so
		// async initialization and periodic refresh can discover them later.
		if checker, ok := p.(core.AvailabilityChecker); ok {
			probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			if err := checker.CheckAvailability(probeCtx); err != nil {
				registry.RecordAvailabilityCheck(name, err)
				slog.Warn("provider unavailable at startup; keeping registered for refresh",
					"name", name,
					"type", pCfg.Type,
					"reason", err.Error())
			} else {
				registry.RecordAvailabilityCheck(name, nil)
			}
			cancel()
		}

		registry.RegisterProviderWithNameAndType(p, name, pCfg.Type)
		if len(pCfg.ModelMetadataOverrides) > 0 {
			registry.SetProviderMetadataOverrides(name, pCfg.ModelMetadataOverrides)
		}
		count++
		slog.Info("provider registered", "name", name, "type", pCfg.Type)
	}

	return count, nil
}
