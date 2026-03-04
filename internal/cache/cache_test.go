package cache

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalCache(t *testing.T) {
	t.Run("GetSetRoundTrip", func(t *testing.T) {
		tmpDir := t.TempDir()
		cacheFile := filepath.Join(tmpDir, "models.json")

		cache := NewLocalCache(cacheFile)
		ctx := context.Background()

		// Initially empty
		result, err := cache.Get(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != nil {
			t.Fatalf("expected nil result for empty cache, got %v", result)
		}

		// Set data
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
		if err != nil {
			t.Fatalf("unexpected error on set: %v", err)
		}

		// Get data back
		result, err = cache.Get(ctx)
		if err != nil {
			t.Fatalf("unexpected error on get: %v", err)
		}
		if result == nil {
			t.Fatal("expected result, got nil")
		}
		p, ok := result.Providers["openai"]
		if !ok || len(p.Models) != 1 {
			t.Fatalf("expected 1 model in openai provider, got %v", result.Providers)
		}
		if p.Models[0].ID != "test-model" {
			t.Errorf("expected test-model in cache, got %q", p.Models[0].ID)
		}
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
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify file was created
		if _, err := os.Stat(cacheFile); os.IsNotExist(err) {
			t.Fatal("cache file was not created")
		}
	})

	t.Run("EmptyFilePath", func(t *testing.T) {
		cache := NewLocalCache("")
		ctx := context.Background()

		// Get should return nil
		result, err := cache.Get(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != nil {
			t.Fatal("expected nil result for empty path")
		}

		// Set should be a no-op
		data := &ModelCache{}
		err = cache.Set(ctx, data)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("CloseIsNoOp", func(t *testing.T) {
		cache := NewLocalCache("/tmp/test.json")
		err := cache.Close()
		if err != nil {
			t.Fatalf("unexpected error on close: %v", err)
		}
	})

	t.Run("InvalidJSON", func(t *testing.T) {
		tmpDir := t.TempDir()
		cacheFile := filepath.Join(tmpDir, "models.json")

		// Write invalid JSON
		if err := os.WriteFile(cacheFile, []byte("not valid json"), 0o644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		cache := NewLocalCache(cacheFile)
		ctx := context.Background()

		_, err := cache.Get(ctx)
		if err == nil {
			t.Fatal("expected error for invalid JSON")
		}
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
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var restored ModelCache
		if err := json.Unmarshal(data, &restored); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if len(restored.Providers) != len(original.Providers) {
			t.Fatalf("provider count mismatch: got %d, want %d", len(restored.Providers), len(original.Providers))
		}
		openai, ok := restored.Providers["openai-main"]
		if !ok || len(openai.Models) == 0 {
			t.Fatalf("expected openai-main provider with models, got %v", restored.Providers)
		}
		if openai.Models[0].ID != "gpt-4" {
			t.Errorf("openai model ID mismatch: got %q, want %q", openai.Models[0].ID, "gpt-4")
		}
		if openai.ProviderType != "openai" {
			t.Errorf("openai provider type mismatch: got %q, want %q", openai.ProviderType, "openai")
		}
		anthropic := restored.Providers["anthropic-main"]
		if anthropic.ProviderType != "anthropic" {
			t.Errorf("anthropic provider type mismatch: got %q, want %q", anthropic.ProviderType, "anthropic")
		}
	})
}
