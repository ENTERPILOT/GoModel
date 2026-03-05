package cache

import (
	"context"
	"testing"
	"time"
)

func TestRedisModelCache_GetSet(t *testing.T) {
	store := NewMapStore()
	defer store.Close()
	c := NewRedisModelCacheWithStore(store, "test:models", time.Hour)
	defer c.Close()

	ctx := context.Background()
	got, err := c.Get(ctx)
	if err != nil {
		t.Fatalf("Get empty: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for empty cache, got %v", got)
	}

	mc := &ModelCache{
		Version:   1,
		UpdatedAt: time.Now(),
		Models: map[string]CachedModel{
			"gpt-4": {ProviderType: "openai", Object: "model", OwnedBy: "openai", Created: 123},
		},
	}
	if err := c.Set(ctx, mc); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err = c.Get(ctx)
	if err != nil {
		t.Fatalf("Get after Set: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil ModelCache")
	}
	if got.Version != 1 {
		t.Errorf("Version: got %d, want 1", got.Version)
	}
	if len(got.Models) != 1 {
		t.Errorf("Models: got %d entries, want 1", len(got.Models))
	}
	m, ok := got.Models["gpt-4"]
	if !ok {
		t.Fatal("expected gpt-4 in Models")
	}
	if m.ProviderType != "openai" {
		t.Errorf("ProviderType: got %s, want openai", m.ProviderType)
	}
}

func TestRedisModelCache_DefaultKeyAndTTL(t *testing.T) {
	store := NewMapStore()
	defer store.Close()
	c := NewRedisModelCacheWithStore(store, "", 0)
	defer c.Close()

	ctx := context.Background()
	mc := &ModelCache{Version: 1, UpdatedAt: time.Now(), Models: map[string]CachedModel{}}
	if err := c.Set(ctx, mc); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := c.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil ModelCache")
	}
}
