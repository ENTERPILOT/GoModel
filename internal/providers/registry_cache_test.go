package providers

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gomodel/config"
	"gomodel/internal/cache/modelcache"
	"gomodel/internal/core"
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
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify cache file was created
		if _, err := os.Stat(cacheFile); os.IsNotExist(err) {
			t.Fatal("cache file was not created")
		}

		// Verify cache file contents
		data, err := os.ReadFile(cacheFile)
		if err != nil {
			t.Fatalf("failed to read cache file: %v", err)
		}

		var modelCache modelcache.ModelCache
		if err := json.Unmarshal(data, &modelCache); err != nil {
			t.Fatalf("failed to unmarshal cache: %v", err)
		}

		p, ok := modelCache.Providers["openai"]
		if !ok {
			t.Fatal("expected openai provider in cache")
		}
		if len(p.Models) != 2 {
			t.Errorf("expected 2 models, got %d", len(p.Models))
		}
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
		if err := os.WriteFile(cacheFile, data, 0o644); err != nil {
			t.Fatalf("failed to write cache file: %v", err)
		}

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
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if loaded != 2 {
			t.Errorf("expected 2 models loaded, got %d", loaded)
		}

		// Verify models are accessible
		if !registry.Supports("gpt-4o") {
			t.Error("expected gpt-4o to be supported")
		}
		if !registry.Supports("claude-3-5-sonnet") {
			t.Error("expected claude-3-5-sonnet to be supported")
		}

		// Verify correct provider mapping
		provider := registry.GetProvider("gpt-4o")
		if provider != openaiMock {
			t.Error("expected gpt-4o to be mapped to openai provider")
		}

		provider = registry.GetProvider("claude-3-5-sonnet")
		if provider != anthropicMock {
			t.Error("expected claude-3-5-sonnet to be mapped to anthropic provider")
		}
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
		if err := os.WriteFile(cacheFile, data, 0o644); err != nil {
			t.Fatalf("failed to write cache file: %v", err)
		}

		registry := NewModelRegistry()
		localCache := modelcache.NewLocalCache(cacheFile)
		registry.SetCache(localCache)

		east := &registryMockProvider{name: "openai-east"}
		west := &registryMockProvider{name: "openai-west"}
		registry.RegisterProviderWithNameAndType(east, "openai-east", "openai")
		registry.RegisterProviderWithNameAndType(west, "openai-west", "openai")

		loaded, err := registry.LoadFromCache(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if loaded != 1 {
			t.Fatalf("expected 1 unqualified model loaded, got %d", loaded)
		}

		if provider := registry.GetProvider("openai-east/gpt-4o"); provider != east {
			t.Fatal("expected openai-east/gpt-4o to map to openai-east provider")
		}
		if provider := registry.GetProvider("openai-west/gpt-4o"); provider != west {
			t.Fatal("expected openai-west/gpt-4o to map to openai-west provider")
		}
		// Unqualified lookup should resolve to one of the two providers (map iteration order is nondeterministic)
		if provider := registry.GetProvider("gpt-4o"); provider != east && provider != west {
			t.Fatal("expected unqualified gpt-4o to map to either openai-east or openai-west provider")
		}
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
		if err := os.WriteFile(cacheFile, data, 0o644); err != nil {
			t.Fatalf("failed to write cache file: %v", err)
		}

		registry := NewModelRegistry()
		registry.SetCache(modelcache.NewLocalCache(cacheFile))
		registry.SetConfiguredProviderModelsMode(config.ConfiguredProviderModelsModeAllowlist)
		registry.SetProviderConfiguredModels("openrouter", []string{"missing-configured", "configured-model"})

		mock := &registryMockProvider{name: "openrouter"}
		registry.RegisterProviderWithNameAndType(mock, "openrouter", "openrouter")

		loaded, err := registry.LoadFromCache(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if loaded != 2 {
			t.Fatalf("expected 2 models loaded, got %d", loaded)
		}
		if registry.Supports("cached-extra") {
			t.Fatal("expected allowlist mode to hide cached-extra")
		}
		configured := registry.GetModel("configured-model")
		if configured == nil {
			t.Fatal("expected configured-model to resolve")
		}
		if configured.Model.Created != 123 || configured.Model.OwnedBy != "openrouter" {
			t.Fatalf("configured metadata = %+v, want cached metadata preserved", configured.Model)
		}
		missing := registry.GetModel("missing-configured")
		if missing == nil {
			t.Fatal("expected missing-configured to resolve")
		}
		if missing.Model.OwnedBy != "openrouter" {
			t.Fatalf("OwnedBy = %q, want openrouter", missing.Model.OwnedBy)
		}
	})

	t.Run("LoadFromCacheConfiguredModelsFallbackUsesConfiguredWhenCachedProviderMissing", func(t *testing.T) {
		tmpDir := t.TempDir()
		cacheFile := filepath.Join(tmpDir, "models.json")

		modelCache := modelcache.ModelCache{
			UpdatedAt: time.Now().UTC(),
			Providers: map[string]modelcache.CachedProvider{},
		}
		data, _ := json.Marshal(modelCache)
		if err := os.WriteFile(cacheFile, data, 0o644); err != nil {
			t.Fatalf("failed to write cache file: %v", err)
		}

		registry := NewModelRegistry()
		registry.SetCache(modelcache.NewLocalCache(cacheFile))
		registry.SetProviderConfiguredModels("vllm", []string{"meta-llama/Llama-3.1-8B-Instruct"})

		mock := &registryMockProvider{name: "vllm"}
		registry.RegisterProviderWithNameAndType(mock, "vllm", "vllm")

		loaded, err := registry.LoadFromCache(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if loaded != 1 {
			t.Fatalf("expected 1 model loaded, got %d", loaded)
		}
		if !registry.Supports("meta-llama/Llama-3.1-8B-Instruct") {
			t.Fatal("expected configured fallback model to be loaded")
		}
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
		if err := os.WriteFile(cacheFile, data, 0o644); err != nil {
			t.Fatalf("failed to write cache file: %v", err)
		}

		registry := NewModelRegistry()
		localCache := modelcache.NewLocalCache(cacheFile)
		registry.SetCache(localCache)

		openaiMock := &registryMockProvider{name: "openai"}
		registry.RegisterProviderWithNameAndType(openaiMock, "openai-main", "openai")

		loaded, err := registry.LoadFromCache(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if loaded != 1 {
			t.Fatalf("expected 1 model loaded, got %d", loaded)
		}

		models := registry.ListModelsWithProvider()
		if len(models) != 1 {
			t.Fatalf("expected 1 model with provider, got %d", len(models))
		}
		if models[0].ProviderType != "openai" {
			t.Fatalf("ProviderType = %q, want %q", models[0].ProviderType, "openai")
		}
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
		if err := os.WriteFile(cacheFile, data, 0o644); err != nil {
			t.Fatalf("failed to write cache file: %v", err)
		}

		registry := NewModelRegistry()
		localCache := modelcache.NewLocalCache(cacheFile)
		registry.SetCache(localCache)

		openaiMock := &registryMockProvider{name: "openai"}
		registry.RegisterProviderWithNameAndType(openaiMock, "openai-main", "openai")

		loaded, err := registry.LoadFromCache(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if loaded != 1 {
			t.Fatalf("expected 1 model loaded, got %d", loaded)
		}

		models := registry.ListModelsWithProvider()
		if len(models) != 1 {
			t.Fatalf("expected 1 model with provider, got %d", len(models))
		}
		if models[0].ProviderType != "openai" {
			t.Fatalf("ProviderType = %q, want %q", models[0].ProviderType, "openai")
		}
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
		if err := os.WriteFile(cacheFile, data, 0o644); err != nil {
			t.Fatalf("failed to write cache file: %v", err)
		}

		registry := NewModelRegistry()
		localCache := modelcache.NewLocalCache(cacheFile)
		registry.SetCache(localCache)

		openrouterMock := &registryMockProvider{name: "openrouter"}
		registry.RegisterProviderWithNameAndType(openrouterMock, "openrouter-main", "")

		loaded, err := registry.LoadFromCache(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if loaded != 1 {
			t.Fatalf("expected 1 model loaded, got %d", loaded)
		}

		info := registry.GetModel("openrouter-main/shared-model")
		if info == nil || info.Model.Metadata == nil {
			t.Fatal("expected cached model metadata to be present")
		}
		if info.Model.Metadata.ContextWindow == nil || *info.Model.Metadata.ContextWindow != 222222 {
			t.Fatalf("ContextWindow = %v, want 222222", info.Model.Metadata.ContextWindow)
		}
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
		if err := os.WriteFile(cacheFile, data, 0o644); err != nil {
			t.Fatalf("failed to write cache file: %v", err)
		}

		registry := NewModelRegistry()
		localCache := modelcache.NewLocalCache(cacheFile)
		registry.SetCache(localCache)

		openrouterMock := &registryMockProvider{name: "openrouter"}
		registry.RegisterProviderWithNameAndType(openrouterMock, "openrouter-main", "")

		if _, err := registry.LoadFromCache(context.Background()); err != nil {
			t.Fatalf("LoadFromCache() error = %v", err)
		}
		if err := registry.SaveToCache(context.Background()); err != nil {
			t.Fatalf("SaveToCache() error = %v", err)
		}

		saved, err := os.ReadFile(cacheFile)
		if err != nil {
			t.Fatalf("failed to read cache file: %v", err)
		}

		var rewritten modelcache.ModelCache
		if err := json.Unmarshal(saved, &rewritten); err != nil {
			t.Fatalf("failed to unmarshal saved cache: %v", err)
		}

		provider, ok := rewritten.Providers["openrouter-main"]
		if !ok {
			t.Fatal("expected openrouter-main provider in saved cache")
		}
		if provider.ProviderType != "openrouter" {
			t.Fatalf("ProviderType = %q, want %q", provider.ProviderType, "openrouter")
		}
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
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Only the OpenAI model should be loaded
		if loaded != 1 {
			t.Errorf("expected 1 model loaded, got %d", loaded)
		}
		if !registry.Supports("gpt-4o") {
			t.Error("expected gpt-4o to be supported")
		}
		if registry.Supports("claude-3") {
			t.Error("expected claude-3 to NOT be supported (unconfigured provider)")
		}
	})

	t.Run("LoadFromCacheNoFile", func(t *testing.T) {
		tmpDir := t.TempDir()
		cacheFile := filepath.Join(tmpDir, "nonexistent.json")

		registry := NewModelRegistry()
		localCache := modelcache.NewLocalCache(cacheFile)
		registry.SetCache(localCache)

		loaded, err := registry.LoadFromCache(context.Background())
		if err != nil {
			t.Fatalf("expected no error for missing file, got: %v", err)
		}
		if loaded != 0 {
			t.Errorf("expected 0 models loaded, got %d", loaded)
		}
	})

	t.Run("LoadFromCacheNoCacheSet", func(t *testing.T) {
		registry := NewModelRegistry()

		loaded, err := registry.LoadFromCache(context.Background())
		if err != nil {
			t.Fatalf("expected no error when no cache set, got: %v", err)
		}
		if loaded != 0 {
			t.Errorf("expected 0 models loaded, got %d", loaded)
		}
	})

	t.Run("SaveToCacheNoCacheSet", func(t *testing.T) {
		registry := NewModelRegistry()

		err := registry.SaveToCache(context.Background())
		if err != nil {
			t.Fatalf("expected no error when no cache set, got: %v", err)
		}
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
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if _, err := os.Stat(cacheFile); os.IsNotExist(err) {
			t.Fatal("cache file was not created in nested directory")
		}
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
		if !registry.Supports("cached-model") {
			t.Error("expected cached-model to be available immediately")
		}

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
		if !registry.Supports("network-model") {
			t.Error("expected network-model to be available after background refresh")
		}

		// Should be marked as initialized
		if !registry.IsInitialized() {
			t.Error("expected registry to be marked as initialized")
		}
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

		// Verify cache file was created
		if _, err := os.Stat(cacheFile); os.IsNotExist(err) {
			t.Fatal("cache file was not created after background refresh")
		}

		// Verify cache contains the network model
		data, _ := os.ReadFile(cacheFile)
		var modelCache modelcache.ModelCache
		_ = json.Unmarshal(data, &modelCache)

		p, ok := modelCache.Providers["test"]
		if !ok {
			t.Fatal("expected test provider in cache")
		}
		if len(p.Models) != 1 {
			t.Fatalf("expected 1 model in cache, got %d", len(p.Models))
		}
		if p.Models[0].ID != "new-model" {
			t.Errorf("expected new-model in cache, got %v", p.Models)
		}
	})
}

func TestIsInitialized(t *testing.T) {
	t.Run("FalseBeforeInitialize", func(t *testing.T) {
		registry := NewModelRegistry()

		if registry.IsInitialized() {
			t.Error("expected IsInitialized to be false before initialization")
		}
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

		if !registry.IsInitialized() {
			t.Error("expected IsInitialized to be true after initialization")
		}
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
		if registry.IsInitialized() {
			t.Error("expected IsInitialized to be false after loading from cache only")
		}
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

	if registry.ProviderCount() != 1 {
		t.Errorf("expected 1 provider, got %d", registry.ProviderCount())
	}
}
