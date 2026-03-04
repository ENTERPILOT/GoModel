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
			Models: []CachedModel{
				{
					ModelID:      "test-model",
					Provider:     "openai",
					ProviderType: "openai",
					Object:       "model",
					OwnedBy:      "openai",
					Created:      1234567890,
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
		if len(result.Models) != 1 {
			t.Errorf("expected 1 model, got %d", len(result.Models))
		}
		if result.Models[0].ModelID != "test-model" {
			t.Errorf("expected test-model in cache, got %q", result.Models[0].ModelID)
		}
	})

	t.Run("CreateDirectoryIfNeeded", func(t *testing.T) {
		tmpDir := t.TempDir()
		cacheFile := filepath.Join(tmpDir, "nested", "dir", "models.json")

		cache := NewLocalCache(cacheFile)
		ctx := context.Background()

		data := &ModelCache{
			Models: []CachedModel{},
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
			Models: []CachedModel{
				{
					ModelID:      "gpt-4",
					Provider:     "openai-main",
					ProviderType: "openai",
					Object:       "model",
					OwnedBy:      "openai",
					Created:      1234567890,
				},
				{
					ModelID:      "claude-3",
					Provider:     "anthropic-main",
					ProviderType: "anthropic",
					Object:       "model",
					OwnedBy:      "anthropic",
					Created:      1234567891,
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

		if len(restored.Models) != len(original.Models) {
			t.Errorf("model count mismatch: got %d, want %d", len(restored.Models), len(original.Models))
		}
		if restored.Models[0].ModelID != original.Models[0].ModelID {
			t.Errorf("first model ID mismatch: got %q, want %q", restored.Models[0].ModelID, original.Models[0].ModelID)
		}
		if restored.Models[0].Provider != original.Models[0].Provider {
			t.Errorf("first provider mismatch: got %q, want %q", restored.Models[0].Provider, original.Models[0].Provider)
		}
		if restored.Models[1].ProviderType != original.Models[1].ProviderType {
			t.Errorf("second provider type mismatch: got %q, want %q", restored.Models[1].ProviderType, original.Models[1].ProviderType)
		}
	})
}
