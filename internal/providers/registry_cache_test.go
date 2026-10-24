package providers

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/cache/modelcache"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/modeldata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCacheFile(t *testing.T) {
	t.Run("SetCache", func(t *testing.T) {
		registry := NewModelRegistry()
		localCache := modelcache.NewLocalCache("/tmp/test-cache.json")
		registry.SetCache(localCache)
		// Verify no panic, cache is set (private field)
	})

	t.Run("SaveToCache", func(t *testing.T) {
		tmpDir := t.TempDir()
		cacheFile := filepath.Join(tmpDir, "models.json")

		registry := NewModelRegistry()
		localCache := modelcache.NewLocalCache(cacheFile)
		registry.SetCache(localCache)

		mock := &registryMockProvider{
			name: "openai",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data: []core.Model{
					{ID: "gpt-4o", Object: "model", OwnedBy: "openai", Created: 1234567890},
					{ID: "gpt-3.5-turbo", Object: "model", OwnedBy: "openai", Created: 1234567891},
				},
			},
		}
		registry.RegisterProviderWithNameAndType(mock, "openai", "openai")
		_ = registry.Initialize(context.Background())

		err := registry.SaveToCache(context.Background())
		require.NoError(t, err)
		_, err = // Verify cache file was created
			os.Stat(cacheFile)
		require.False(t, os.IsNotExist(err))

		// Verify cache file contents
		data, err := os.ReadFile(cacheFile)
		require.NoError(t, err)

		var modelCache modelcache.ModelCache
		err = json.Unmarshal(data, &modelCache)
		require.NoError(t, err)

		p, ok := modelCache.Providers["openai"]
		require.True(t, ok)
		assert.Len(t, p.Models, 2)
	})

	t.Run("LoadFromCache", func(t *testing.T) {
		tmpDir := t.TempDir()
		cacheFile := filepath.Join(tmpDir, "models.json")

		// Create a cache file
		modelCache := modelcache.ModelCache{
			UpdatedAt: time.Now().UTC(),
			Providers: map[string]modelcache.CachedProvider{
				"openai-main": {
					ProviderType: "openai",
					OwnedBy:      "openai",
					Models: []modelcache.CachedModel{
						{ID: "gpt-4o", Created: 1234567890},
					},
				},
				"anthropic-main": {
					ProviderType: "anthropic",
					OwnedBy:      "anthropic",
					Models: []modelcache.CachedModel{
						{ID: "claude-3-5-sonnet", Created: 1234567891},
					},
				},
			},
		}
		data, _ := json.Marshal(modelCache)
		err := os.WriteFile(cacheFile, data, 0o644)
		require.NoError(t, err)

		// Create registry with providers
		registry := NewModelRegistry()
		localCache := modelcache.NewLocalCache(cacheFile)
		registry.SetCache(localCache)

		openaiMock := &registryMockProvider{
			name:           "openai",
			modelsResponse: &core.ModelsResponse{Object: "list"},
		}
		anthropicMock := &registryMockProvider{
			name:           "anthropic",
			modelsResponse: &core.ModelsResponse{Object: "list"},
		}
		registry.RegisterProviderWithNameAndType(openaiMock, "openai-main", "openai")
		registry.RegisterProviderWithNameAndType(anthropicMock, "anthropic-main", "anthropic")

		// Load from cache
		loaded, err := registry.LoadFromCache(context.Background())
		require.NoError(t, err)
		assert.Equal(t, 2, loaded)

		// Verify models are accessible
		assert.True(t, registry.Supports("gpt-4o"))
		assert.True(t, registry.Supports("claude-3-5-sonnet"))

		// Verify correct provider mapping
		provider := registry.GetProvider("gpt-4o")
		assert.Equal(t, openaiMock, provider)

		provider = registry.GetProvider("claude-3-5-sonnet")
		assert.Equal(t, anthropicMock, provider)
	})

	t.Run("LoadFromCachePreservesProviderInstancesWithSameType", func(t *testing.T) {
		tmpDir := t.TempDir()
		cacheFile := filepath.Join(tmpDir, "models.json")

		modelCache := modelcache.ModelCache{
			UpdatedAt: time.Now().UTC(),
			Providers: map[string]modelcache.CachedProvider{
				"openai-east": {
					ProviderType: "openai",
					OwnedBy:      "openai",
					Models: []modelcache.CachedModel{
						{ID: "gpt-4o"},
					},
				},
				"openai-west": {
					ProviderType: "openai",
					OwnedBy:      "openai",
					Models: []modelcache.CachedModel{
						{ID: "gpt-4o"},
					},
				},
			},
		}
		data, _ := json.Marshal(modelCache)
		err := os.WriteFile(cacheFile, data, 0o644)
		require.NoError(t, err)

		registry := NewModelRegistry()
		localCache := modelcache.NewLocalCache(cacheFile)
		registry.SetCache(localCache)

		east := &registryMockProvider{name: "openai-east"}
		west := &registryMockProvider{name: "openai-west"}
		registry.RegisterProviderWithNameAndType(east, "openai-east", "openai")
		registry.RegisterProviderWithNameAndType(west, "openai-west", "openai")

		loaded, err := registry.LoadFromCache(context.Background())
		require.NoError(t, err)
		require.Equal(t, 1, loaded)
		provider := registry.GetProvider("openai-east/gpt-4o")
		require.Equal(t, east, provider)
		provider = registry.GetProvider("openai-west/gpt-4o")
		require.Equal(t, west, provider)

		// Unqualified lookup should resolve to one of the two providers (map iteration order is nondeterministic)
		require.Contains(t, []core.Provider{east, west}, registry.GetProvider("gpt-4o"))
	})

	t.Run("LoadFromCacheConfiguredModelsAllowlistFiltersAndAdds", func(t *testing.T) {
		tmpDir := t.TempDir()
		cacheFile := filepath.Join(tmpDir, "models.json")

		modelCache := modelcache.ModelCache{
			UpdatedAt: time.Now().UTC(),
			Providers: map[string]modelcache.CachedProvider{
				"openrouter": {
					ProviderType: "openrouter",
					OwnedBy:      "openrouter",
					Models: []modelcache.CachedModel{
						{ID: "configured-model", Created: 123},
						{ID: "cached-extra", Created: 456},
					},
				},
			},
		}
		data, _ := json.Marshal(modelCache)
		err := os.WriteFile(cacheFile, data, 0o644)
		require.NoError(t, err)

		registry := NewModelRegistry()
		registry.SetCache(modelcache.NewLocalCache(cacheFile))
		registry.SetConfiguredProviderModelsMode(config.ConfiguredProviderModelsModeAllowlist)
		registry.SetProviderConfiguredModels("openrouter", []string{"missing-configured", "configured-model"})

		mock := &registryMockProvider{name: "openrouter"}
		registry.RegisterProviderWithNameAndType(mock, "openrouter", "openrouter")

		loaded, err := registry.LoadFromCache(context.Background())
		require.NoError(t, err)
		require.Equal(t, 2, loaded)
		require.False(t, registry.Supports("cached-extra"))

		configured := registry.GetModel("configured-model")
		require.NotNil(t, configured)
		require.Equal(t, int64(123), configured.Model.Created)
		require.Equal(t, "openrouter", configured.Model.OwnedBy, "configured metadata = %+v, want cached metadata preserved", configured.Model)

		missing := registry.GetModel("missing-configured")
		require.NotNil(t, missing)
		require.Equal(t, "openrouter", missing.Model.OwnedBy)
	})

	t.Run("LoadFromCacheConfiguredModelsFallbackUsesConfiguredWhenCachedProviderMissing", func(t *testing.T) {
		tmpDir := t.TempDir()
		cacheFile := filepath.Join(tmpDir, "models.json")

		modelCache := modelcache.ModelCache{
			UpdatedAt: time.Now().UTC(),
			Providers: map[string]modelcache.CachedProvider{},
		}
		data, _ := json.Marshal(modelCache)
		err := os.WriteFile(cacheFile, data, 0o644)
		require.NoError(t, err)

		registry := NewModelRegistry()
		registry.SetCache(modelcache.NewLocalCache(cacheFile))
		registry.SetProviderConfiguredModels("vllm", []string{"meta-llama/Llama-3.1-8B-Instruct"})

		mock := &registryMockProvider{name: "vllm"}
		registry.RegisterProviderWithNameAndType(mock, "vllm", "vllm")

		loaded, err := registry.LoadFromCache(context.Background())
		require.NoError(t, err)
		require.Equal(t, 1, loaded)
		require.True(t, registry.Supports("meta-llama/Llama-3.1-8B-Instruct"))
	})

	t.Run("LoadFromCacheBackfillsMissingProviderTypeFromConfiguredProvider", func(t *testing.T) {
		tmpDir := t.TempDir()
		cacheFile := filepath.Join(tmpDir, "models.json")

		modelCache := modelcache.ModelCache{
			UpdatedAt: time.Now().UTC(),
			Providers: map[string]modelcache.CachedProvider{
				"openai-main": {
					OwnedBy: "openai",
					Models: []modelcache.CachedModel{
						{ID: "gpt-4o"},
					},
				},
			},
		}
		data, _ := json.Marshal(modelCache)
		err := os.WriteFile(cacheFile, data, 0o644)
		require.NoError(t, err)

		registry := NewModelRegistry()
		localCache := modelcache.NewLocalCache(cacheFile)
		registry.SetCache(localCache)

		openaiMock := &registryMockProvider{name: "openai"}
		registry.RegisterProviderWithNameAndType(openaiMock, "openai-main", "openai")

		loaded, err := registry.LoadFromCache(context.Background())
		require.NoError(t, err)
		require.Equal(t, 1, loaded)

		models := registry.ListModelsWithProvider()
		require.Len(t, models, 1)
		require.Equal(t, "openai", models[0].ProviderType)
	})

	t.Run("LoadFromCachePrefersConfiguredProviderTypeOverCachedValue", func(t *testing.T) {
		tmpDir := t.TempDir()
		cacheFile := filepath.Join(tmpDir, "models.json")

		modelCache := modelcache.ModelCache{
			UpdatedAt: time.Now().UTC(),
			Providers: map[string]modelcache.CachedProvider{
				"openai-main": {
					ProviderType: "stale-type",
					OwnedBy:      "openai",
					Models: []modelcache.CachedModel{
						{ID: "gpt-4o"},
					},
				},
			},
		}
		data, _ := json.Marshal(modelCache)
		err := os.WriteFile(cacheFile, data, 0o644)
		require.NoError(t, err)

		registry := NewModelRegistry()
		localCache := modelcache.NewLocalCache(cacheFile)
		registry.SetCache(localCache)

		openaiMock := &registryMockProvider{name: "openai"}
		registry.RegisterProviderWithNameAndType(openaiMock, "openai-main", "openai")

		loaded, err := registry.LoadFromCache(context.Background())
		require.NoError(t, err)
		require.Equal(t, 1, loaded)

		models := registry.ListModelsWithProvider()
		require.Len(t, models, 1)
		require.Equal(t, "openai", models[0].ProviderType)
	})

	t.Run("LoadFromCacheUsesStoredProviderTypeForMetadataEnrichment", func(t *testing.T) {
		tmpDir := t.TempDir()
		cacheFile := filepath.Join(tmpDir, "models.json")

		raw := []byte(`{
			"version": 1,
			"updated_at": "2025-01-01T00:00:00Z",
			"providers": {
				"openrouter": {"display_name": "OpenRouter", "api_type": "openai", "supported_modes": ["chat"]}
			},
			"models": {
				"shared-model": {"display_name": "Shared Model", "modes": ["chat"]}
			},
			"provider_models": {
				"openrouter/shared-model": {"model_ref": "shared-model", "enabled": true, "context_window": 222222}
			}
		}`)

		modelCache := modelcache.ModelCache{
			UpdatedAt:     time.Now().UTC(),
			ModelListData: raw,
			Providers: map[string]modelcache.CachedProvider{
				"openrouter-main": {
					ProviderType: "openrouter",
					OwnedBy:      "openrouter",
					Models: []modelcache.CachedModel{
						{ID: "shared-model"},
					},
				},
			},
		}
		data, _ := json.Marshal(modelCache)
		err := os.WriteFile(cacheFile, data, 0o644)
		require.NoError(t, err)

		registry := NewModelRegistry()
		localCache := modelcache.NewLocalCache(cacheFile)
		registry.SetCache(localCache)

		openrouterMock := &registryMockProvider{name: "openrouter"}
		registry.RegisterProviderWithNameAndType(openrouterMock, "openrouter-main", "")

		loaded, err := registry.LoadFromCache(context.Background())
		require.NoError(t, err)
		require.Equal(t, 1, loaded)

		info := registry.GetModel("openrouter-main/shared-model")
		require.NotNil(t, info)
		require.NotNil(t, info.Model.Metadata)
		require.NotNil(t, info.Model.Metadata.ContextWindow)
		require.Equal(t, 222222, *info.Model.Metadata.ContextWindow)
	})

	t.Run("SaveToCachePrefersStoredProviderTypeOverConfiguredFallback", func(t *testing.T) {
		tmpDir := t.TempDir()
		cacheFile := filepath.Join(tmpDir, "models.json")

		modelCache := modelcache.ModelCache{
			UpdatedAt: time.Now().UTC(),
			Providers: map[string]modelcache.CachedProvider{
				"openrouter-main": {
					ProviderType: "openrouter",
					OwnedBy:      "openrouter",
					Models: []modelcache.CachedModel{
						{ID: "shared-model"},
					},
				},
			},
		}
		data, _ := json.Marshal(modelCache)
		err := os.WriteFile(cacheFile, data, 0o644)
		require.NoError(t, err)

		registry := NewModelRegistry()
		localCache := modelcache.NewLocalCache(cacheFile)
		registry.SetCache(localCache)

		openrouterMock := &registryMockProvider{name: "openrouter"}
		registry.RegisterProviderWithNameAndType(openrouterMock, "openrouter-main", "")
		_, err = registry.LoadFromCache(context.Background())
		require.NoError(t, err)
		err = registry.SaveToCache(context.Background())
		require.NoError(t, err)

		saved, err := os.ReadFile(cacheFile)
		require.NoError(t, err)

		var rewritten modelcache.ModelCache
		err = json.Unmarshal(saved, &rewritten)
		require.NoError(t, err)

		provider, ok := rewritten.Providers["openrouter-main"]
		require.True(t, ok)
		require.Equal(t, "openrouter", provider.ProviderType)
	})

	t.Run("LoadFromCacheSkipsUnconfiguredProviders", func(t *testing.T) {
		tmpDir := t.TempDir()
		cacheFile := filepath.Join(tmpDir, "models.json")

		// Create cache with models from multiple providers
		modelCache := modelcache.ModelCache{
			UpdatedAt: time.Now().UTC(),
			Providers: map[string]modelcache.CachedProvider{
				"openai-main": {
					ProviderType: "openai",
					OwnedBy:      "openai",
					Models: []modelcache.CachedModel{
						{ID: "gpt-4o"},
					},
				},
				"anthropic-main": {
					ProviderType: "anthropic",
					OwnedBy:      "anthropic",
					Models: []modelcache.CachedModel{
						{ID: "claude-3"},
					},
				},
			},
		}
		data, _ := json.Marshal(modelCache)
		_ = os.WriteFile(cacheFile, data, 0o644)

		// Only register OpenAI provider
		registry := NewModelRegistry()
		localCache := modelcache.NewLocalCache(cacheFile)
		registry.SetCache(localCache)
		openaiMock := &registryMockProvider{name: "openai"}
		registry.RegisterProviderWithNameAndType(openaiMock, "openai-main", "openai")

		loaded, err := registry.LoadFromCache(context.Background())
		require.NoError(t, err)

		// Only the OpenAI model should be loaded
		assert.Equal(t, 1, loaded)
		assert.True(t, registry.Supports("gpt-4o"))
		assert.False(t, registry.Supports("claude-3"))
	})

	t.Run("LoadFromCacheNoFile", func(t *testing.T) {
		tmpDir := t.TempDir()
		cacheFile := filepath.Join(tmpDir, "nonexistent.json")

		registry := NewModelRegistry()
		localCache := modelcache.NewLocalCache(cacheFile)
		registry.SetCache(localCache)

		loaded, err := registry.LoadFromCache(context.Background())
		require.NoError(t, err)
		assert.Equal(t, 0, loaded)
	})

	t.Run("LoadFromCacheNoCacheSet", func(t *testing.T) {
		registry := NewModelRegistry()

		loaded, err := registry.LoadFromCache(context.Background())
		require.NoError(t, err)
		assert.Equal(t, 0, loaded)
	})

	t.Run("SaveToCacheNoCacheSet", func(t *testing.T) {
		registry := NewModelRegistry()

		err := registry.SaveToCache(context.Background())
		require.NoError(t, err)
	})

	t.Run("SaveToCacheCreatesDirectory", func(t *testing.T) {
		tmpDir := t.TempDir()
		cacheFile := filepath.Join(tmpDir, "subdir", "nested", "models.json")

		registry := NewModelRegistry()
		localCache := modelcache.NewLocalCache(cacheFile)
		registry.SetCache(localCache)

		mock := &registryMockProvider{
			name: "test",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data: []core.Model{
					{ID: "test-model", Object: "model", OwnedBy: "test"},
				},
			},
		}
		registry.RegisterProviderWithType(mock, "test")
		_ = registry.Initialize(context.Background())

		err := registry.SaveToCache(context.Background())
		require.NoError(t, err)
		_, err = os.Stat(cacheFile)
		require.False(t, os.IsNotExist(err))
	})
}

func TestInitializeAsync(t *testing.T) {
	t.Run("LoadsFromCacheImmediately", func(t *testing.T) {
		tmpDir := t.TempDir()
		cacheFile := filepath.Join(tmpDir, "models.json")

		// Create a cache file
		modelCache := modelcache.ModelCache{
			UpdatedAt: time.Now().UTC(),
			Providers: map[string]modelcache.CachedProvider{
				"test": {
					ProviderType: "test",
					OwnedBy:      "test",
					Models: []modelcache.CachedModel{
						{ID: "cached-model"},
					},
				},
			},
		}
		data, _ := json.Marshal(modelCache)
		_ = os.WriteFile(cacheFile, data, 0o644)

		// Create registry with slow provider (delay ensures cache check happens before network fetch)
		registry := NewModelRegistry()
		localCache := modelcache.NewLocalCache(cacheFile)
		registry.SetCache(localCache)

		mock := &registryMockProvider{
			name:            "test",
			listModelsDelay: 50 * time.Millisecond, // delay long enough for assertion to run
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data: []core.Model{
					{ID: "network-model", Object: "model", OwnedBy: "test"},
				},
			},
		}
		registry.RegisterProviderWithNameAndType(mock, "test", "test")

		// InitializeAsync should return immediately after loading cache
		registry.InitializeAsync(context.Background())

		// Cached model should be available immediately (before background fetch completes)
		assert.True(t, registry.Supports("cached-model"))

		// Wait for background goroutine to complete (for temp dir cleanup)
		time.Sleep(100 * time.Millisecond)
	})

	t.Run("RefreshesInBackground", func(t *testing.T) {
		tmpDir := t.TempDir()
		cacheFile := filepath.Join(tmpDir, "models.json")

		registry := NewModelRegistry()
		localCache := modelcache.NewLocalCache(cacheFile)
		registry.SetCache(localCache)

		mock := &registryMockProvider{
			name: "test",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data: []core.Model{
					{ID: "network-model", Object: "model", OwnedBy: "test"},
				},
			},
		}
		registry.RegisterProviderWithNameAndType(mock, "test", "test")

		// InitializeAsync should start background fetch
		registry.InitializeAsync(context.Background())

		// Wait for background initialization
		time.Sleep(100 * time.Millisecond)

		// Network model should be available after background refresh
		assert.True(t, registry.Supports("network-model"))

		// Should be marked as initialized
		assert.True(t, registry.IsInitialized())
	})

	t.Run("SavesToCacheAfterRefresh", func(t *testing.T) {
		tmpDir := t.TempDir()
		cacheFile := filepath.Join(tmpDir, "models.json")

		registry := NewModelRegistry()
		localCache := modelcache.NewLocalCache(cacheFile)
		registry.SetCache(localCache)

		mock := &registryMockProvider{
			name: "test",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data: []core.Model{
					{ID: "new-model", Object: "model", OwnedBy: "test"},
				},
			},
		}
		registry.RegisterProviderWithNameAndType(mock, "test", "test")

		// InitializeAsync should save to cache after network fetch
		registry.InitializeAsync(context.Background())

		// Wait for background initialization and cache save
		time.Sleep(100 * time.Millisecond)
		_, err := // Verify cache file was created
			os.Stat(cacheFile)
		require.False(t, os.IsNotExist(err))

		// Verify cache contains the network model
		data, _ := os.ReadFile(cacheFile)
		var modelCache modelcache.ModelCache
		_ = json.Unmarshal(data, &modelCache)

		p, ok := modelCache.Providers["test"]
		require.True(t, ok)
		require.Len(t, p.Models, 1)
		assert.Equal(t, "new-model", p.Models[0].ID, "expected new-model in cache, got %v", p.Models)
	})
}

func TestIsInitialized(t *testing.T) {
	t.Run("FalseBeforeInitialize", func(t *testing.T) {
		registry := NewModelRegistry()

		assert.False(t, registry.IsInitialized())
	})

	t.Run("TrueAfterInitialize", func(t *testing.T) {
		registry := NewModelRegistry()
		mock := &registryMockProvider{
			name: "test",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data: []core.Model{
					{ID: "test-model", Object: "model", OwnedBy: "test"},
				},
			},
		}
		registry.RegisterProvider(mock)

		_ = registry.Initialize(context.Background())

		assert.True(t, registry.IsInitialized())
	})

	t.Run("FalseAfterLoadFromCacheOnly", func(t *testing.T) {
		tmpDir := t.TempDir()
		cacheFile := filepath.Join(tmpDir, "models.json")

		// Create a cache file
		modelCache := modelcache.ModelCache{
			UpdatedAt: time.Now().UTC(),
			Providers: map[string]modelcache.CachedProvider{
				"test": {
					ProviderType: "test",
					OwnedBy:      "test",
					Models: []modelcache.CachedModel{
						{ID: "cached-model"},
					},
				},
			},
		}
		data, _ := json.Marshal(modelCache)
		_ = os.WriteFile(cacheFile, data, 0o644)

		registry := NewModelRegistry()
		localCache := modelcache.NewLocalCache(cacheFile)
		registry.SetCache(localCache)
		mock := &registryMockProvider{name: "test"}
		registry.RegisterProviderWithNameAndType(mock, "test", "test")

		_, _ = registry.LoadFromCache(context.Background())

		// Should not be marked as initialized (only loaded from cache)
		assert.False(t, registry.IsInitialized())
	})
}

func TestRegisterProviderWithType(t *testing.T) {
	registry := NewModelRegistry()

	mock := &registryMockProvider{
		name: "test",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "test-model", Object: "model", OwnedBy: "test"},
			},
		},
	}

	registry.RegisterProviderWithType(mock, "openai")

	assert.Equal(t, 1, registry.ProviderCount())
}

// SaveToCache must not persist a stale (carried-forward) inventory: an
// offline provider's models would otherwise resurrect from the cache on every
// restart and stay advertised forever (issue #705).
func TestSaveToCache_SkipsStaleProviderInventory(t *testing.T) {
	tmpDir := t.TempDir()
	cacheFile := filepath.Join(tmpDir, "models.json")

	registry, _, beta := registerTwoProviderRegistry(t)
	registry.SetCache(modelcache.NewLocalCache(cacheFile))

	beta.err = errors.New("connection refused")
	err := registry.Initialize(context.Background())
	require.NoError(t, err)
	err = registry.SaveToCache(context.Background())
	require.NoError(t, err)

	data, err := os.ReadFile(cacheFile)
	require.NoError(t, err)

	var modelCache modelcache.ModelCache
	err = json.Unmarshal(data, &modelCache)
	require.NoError(t, err)
	_, ok := modelCache.Providers["beta"]
	assert.False(t, ok)
	_, ok = modelCache.Providers["alpha"]
	assert.True(t, ok)

	// After recovery the provider re-enters the cache.
	beta.err = nil
	err = registry.Initialize(context.Background())
	require.NoError(t, err)
	err = registry.SaveToCache(context.Background())
	require.NoError(t, err)

	data, err = os.ReadFile(cacheFile)
	require.NoError(t, err)
	err = json.Unmarshal(data, &modelCache)
	require.NoError(t, err)
	_, ok = modelCache.Providers["beta"]
	assert.True(t, ok)
}

func TestCacheFile_ModelListETagRoundtrip(t *testing.T) {
	tmpDir := t.TempDir()
	cacheFile := filepath.Join(tmpDir, "models.json")

	raw := []byte(`{"version": 1, "providers": {}, "models": {"m": {"display_name": "M", "modes": ["chat"]}}, "provider_models": {}}`)
	list, err := modeldata.Parse(raw)
	require.NoError(t, err)

	saving := NewModelRegistry()
	saving.SetCache(modelcache.NewLocalCache(cacheFile))
	mock := &registryMockProvider{
		name: "openai",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data:   []core.Model{{ID: "gpt-4o", Object: "model", OwnedBy: "openai"}},
		},
	}
	saving.RegisterProviderWithNameAndType(mock, "openai", "openai")
	_ = saving.Initialize(context.Background())
	saving.setModelListAndEnrich(list, raw, `"list-v7"`, "https://example.test/models.min.json")
	err = saving.SaveToCache(context.Background())
	require.NoError(t, err)

	loading := NewModelRegistry()
	loading.SetCache(modelcache.NewLocalCache(cacheFile))
	loading.RegisterProviderWithNameAndType(mock, "openai", "openai")
	_, err = loading.LoadFromCache(context.Background())
	require.NoError(t, err)
	got := loading.currentModelListETag("https://example.test/models.min.json")
	require.Equal(t, `"list-v7"`, got)
	got = loading.currentModelListETag("https://other.test/models.min.json")
	require.Empty(t, got)
}

// A restart must not swap the provider's own report for the catalog's entry.
// The catalog resolves plain IDs like "gpt-oss" for any provider type, and its
// entry for one carries no context window, output limit, or capabilities — so a
// cached inventory that lost the provider's report publishes a local model with
// a truncated mode list and no limits at all until the next live fetch lands.
func TestCacheRoundTripKeepsProviderReportedMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	cacheFile := filepath.Join(tmpDir, "models.json")

	raw := []byte(`{"version": 1, "providers": {}, "models": {"gpt-oss": {"display_name": "gpt-oss", "description": "open-weight models", "modes": ["chat"]}}, "provider_models": {}}`)
	list, err := modeldata.Parse(raw)
	require.NoError(t, err)

	contextWindow := 922000
	maxOutputTokens := 128000
	mock := &registryMockProvider{
		name: "local",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{{
				ID: "gpt-oss", Object: "model", OwnedBy: "local",
				Metadata: &core.ModelMetadata{
					DisplayName:     "My Local gpt-oss",
					Modes:           []string{"chat", "responses"},
					ContextWindow:   &contextWindow,
					MaxOutputTokens: &maxOutputTokens,
					Capabilities:    map[string]bool{"function_calling": true, "reasoning": true},
				},
			}},
		},
	}

	saving := NewModelRegistry()
	saving.SetCache(modelcache.NewLocalCache(cacheFile))
	saving.RegisterProviderWithNameAndType(mock, "local", "openai")
	err = saving.Initialize(context.Background())
	require.NoError(t, err)

	saving.setModelListAndEnrich(list, raw, "", "")
	err = saving.SaveToCache(context.Background())
	require.NoError(t, err)

	// A fresh registry that has not reached the provider yet: everything it
	// publishes comes from the cache plus the cached catalog.
	loading := NewModelRegistry()
	loading.SetCache(modelcache.NewLocalCache(cacheFile))
	loading.RegisterProviderWithNameAndType(mock, "local", "openai")
	_, err = loading.LoadFromCache(context.Background())
	require.NoError(t, err)

	model, ok := loading.LookupModel("local/gpt-oss")
	require.True(t, ok)

	meta := model.Metadata
	require.NotNil(t, meta)
	require.NotNil(t, meta.ContextWindow)
	assert.Equal(t, contextWindow, *meta.ContextWindow)
	require.NotNil(t, meta.MaxOutputTokens)
	assert.Equal(t, maxOutputTokens, *meta.MaxOutputTokens)
	assert.True(t, meta.Capabilities["function_calling"])
	assert.True(t, meta.Capabilities["reasoning"], "Capabilities = %v, want the provider-reported flags", meta.Capabilities)
	require.Len(t, meta.Modes, 2)
	assert.Equal(t, "chat", meta.Modes[0])
	assert.Equal(t, "responses", meta.Modes[1])
	assert.Equal(t, "My Local gpt-oss", meta.DisplayName)

	// The catalog still fills in what the provider never reported.
	assert.Equal(t, "open-weight models", meta.Description)
}

// Caches written before provider metadata was persisted carry none; they must
// still load, with the catalog supplying what it can until the next refresh.
func TestLoadFromCacheAcceptsEntriesWithoutMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	cacheFile := filepath.Join(tmpDir, "models.json")

	modelCache := modelcache.ModelCache{
		UpdatedAt: time.Now().UTC(),
		Providers: map[string]modelcache.CachedProvider{
			"local": {
				ProviderType: "openai",
				OwnedBy:      "local",
				Models:       []modelcache.CachedModel{{ID: "gpt-oss", Created: 1234567890}},
			},
		},
	}
	data, _ := json.Marshal(modelCache)
	err := os.WriteFile(cacheFile, data, 0o644)
	require.NoError(t, err)

	registry := NewModelRegistry()
	registry.SetCache(modelcache.NewLocalCache(cacheFile))
	registry.RegisterProviderWithNameAndType(&registryMockProvider{name: "local"}, "local", "openai")

	loaded, err := registry.LoadFromCache(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, loaded)

	registry.mu.RLock()
	info, ok := registry.models["gpt-oss"]
	registry.mu.RUnlock()
	require.True(t, ok)
	assert.Nil(t, info.Discovered)
}
