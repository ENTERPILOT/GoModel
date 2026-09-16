package modelcache

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocalCache(t *testing.T) {
	t.Run("GetSetRoundTrip", func(t *testing.T) {
		tmpDir := t.TempDir()
		cacheFile := filepath.Join(tmpDir, "models.json")

		cache := NewLocalCache(cacheFile)
		ctx := context.Background()

		result, err := cache.Get(ctx)
		require.NoError(t, err)
		require.Nil(t, result)

		data := &ModelCache{
			UpdatedAt: time.Now().UTC(),
			Providers: map[string]CachedProvider{
				"openai": {
					ProviderType: "openai",
					OwnedBy:      "openai",
					Models: []CachedModel{
						{ID: "test-model", Created: 1234567890},
					},
				},
			},
		}

		err = cache.Set(ctx, data)
		require.NoError(t, err)

		result, err = cache.Get(ctx)
		require.NoError(t, err)

		require.NotNil(t, result, "expected result, got nil")
		p, ok := result.Providers["openai"]
		require.True(t, ok)
		require.Len(t, p.Models, 1, "expected 1 model in openai provider, got %v", result.Providers)
		assert.Equal(t, "test-model", p.Models[0].ID)
	})

	t.Run("CreateDirectoryIfNeeded", func(t *testing.T) {
		tmpDir := t.TempDir()
		cacheFile := filepath.Join(tmpDir, "nested", "dir", "models.json")

		cache := NewLocalCache(cacheFile)
		ctx := context.Background()

		data := &ModelCache{
			Providers: map[string]CachedProvider{},
		}

		err := cache.Set(ctx, data)
		require.NoError(t, err)
		_, err = os.Stat(cacheFile)
		require.False(t, os.IsNotExist(err))
	})

	t.Run("EmptyFilePath", func(t *testing.T) {
		cache := NewLocalCache("")
		ctx := context.Background()

		result, err := cache.Get(ctx)
		require.NoError(t, err)
		require.Nil(t, result)

		data := &ModelCache{}
		err = cache.Set(ctx, data)
		require.NoError(t, err)
	})

	t.Run("CloseIsNoOp", func(t *testing.T) {
		cache := NewLocalCache("/tmp/test.json")
		err := cache.Close()
		require.NoError(t, err)
	})

	t.Run("InvalidJSON", func(t *testing.T) {
		tmpDir := t.TempDir()
		cacheFile := filepath.Join(tmpDir, "models.json")
		err := os.WriteFile(cacheFile, []byte("not valid json"), 0o644)
		require.NoError(t, err)

		cache := NewLocalCache(cacheFile)
		ctx := context.Background()

		_, err = cache.Get(ctx)
		require.Error(t, err)
	})
}

func TestModelCacheSerialization(t *testing.T) {
	t.Run("JSONRoundTrip", func(t *testing.T) {
		original := &ModelCache{
			UpdatedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			Providers: map[string]CachedProvider{
				"openai-main": {
					ProviderType: "openai",
					OwnedBy:      "openai",
					Models: []CachedModel{
						{ID: "gpt-4", Created: 1234567890},
					},
				},
				"anthropic-main": {
					ProviderType: "anthropic",
					OwnedBy:      "anthropic",
					Models: []CachedModel{
						{ID: "claude-3", Created: 1234567891},
					},
				},
			},
		}

		data, err := json.Marshal(original)
		require.NoError(t, err)

		var restored ModelCache
		err = json.Unmarshal(data, &restored)
		require.NoError(t, err)
		require.Len(t, restored.Providers, len(original.Providers))

		openai, ok := restored.Providers["openai-main"]
		require.True(t, ok)
		require.NotEmpty(t, openai.Models, "expected openai-main provider with models, got %v", restored.Providers)
		assert.Equal(t, "gpt-4", openai.Models[0].ID)
		assert.Equal(t, "openai", openai.ProviderType)

		anthropic := restored.Providers["anthropic-main"]
		assert.Equal(t, "anthropic", anthropic.ProviderType)
	})
}
