package modelcache

import (
	"context"
	"testing"
	"time"

	"gomodel/internal/cache"
)

func TestRedisModelCache_GetSet(t *testing.T) {
	store := cache.NewMapStore()
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
		UpdatedAt: time.Now(),
		Providers: map[string]CachedProvider{
			"openai": {
				ProviderType: "openai",
				OwnedBy:      "openai",
				Models: []CachedModel{
					{ID: "gpt-4", Created: 123},
				},
			},
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
	if len(got.Providers) != 1 {
		t.Errorf("Providers: got %d entries, want 1", len(got.Providers))
	}
	p, ok := got.Providers["openai"]
	if !ok {
		t.Fatal("expected openai in Providers")
	}
	if p.ProviderType != "openai" {
		t.Errorf("ProviderType: got %s, want openai", p.ProviderType)
	}
	if len(p.Models) != 1 {
		t.Errorf("Models: got %d entries, want 1", len(p.Models))
	}
	if p.Models[0].ID != "gpt-4" {
		t.Errorf("Model ID: got %s, want gpt-4", p.Models[0].ID)
	}
}

func TestRedisModelCache_DefaultKeyAndTTL(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()
	c := NewRedisModelCacheWithStore(store, "", 0)
	defer c.Close()

	rc, ok := c.(*redisModelCache)
	if !ok {
		t.Fatal("expected *redisModelCache from NewRedisModelCacheWithStore")
	}
	if rc.key != DefaultRedisKey {
		t.Errorf("key = %q, want %q", rc.key, DefaultRedisKey)
	}
	if rc.ttl != cache.DefaultRedisTTL {
		t.Errorf("ttl = %v, want %v", rc.ttl, cache.DefaultRedisTTL)
	}

	ctx := context.Background()
	mc := &ModelCache{
		UpdatedAt: time.Now(),
		Providers: map[string]CachedProvider{},
	}
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

func TestRedisModelCacheWithStore_CloseDoesNotCloseSharedStore(t *testing.T) {
	spy := &spyStore{}
	c := NewRedisModelCacheWithStore(spy, "test:models", time.Hour)

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if spy.closeCalls != 0 {
		t.Errorf("shared store Close called %d time(s), want 0", spy.closeCalls)
	}
}

func TestRedisModelCache_CloseClosesOwnedStore(t *testing.T) {
	spy := &spyStore{}
	c := &redisModelCache{store: spy, key: DefaultRedisKey, ttl: cache.DefaultRedisTTL, owned: true}

	if err := c.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if spy.closeCalls != 1 {
		t.Errorf("owned store Close called %d time(s) after first Close, want 1", spy.closeCalls)
	}

	// Second Close must not panic or error.
	if err := c.Close(); err != nil {
		t.Errorf("second Close on owned cache: %v", err)
	}
	if spy.closeCalls != 2 {
		t.Errorf("owned store Close called %d time(s) after second Close, want 2", spy.closeCalls)
	}
}

// spyStore is a cache.Store that records how many times Close and Set have been called.
type spyStore struct {
	closeCalls int
	setCalls   int
}

func (s *spyStore) Get(_ context.Context, _ string) ([]byte, error)                  { return nil, nil }
func (s *spyStore) Set(_ context.Context, _ string, _ []byte, _ time.Duration) error { s.setCalls++; return nil }
func (s *spyStore) Close() error                                                      { s.closeCalls++; return nil }

func TestRedisModelCache_SetNilReturnsError(t *testing.T) {
	spy := &spyStore{}
	c := &redisModelCache{store: spy, key: DefaultRedisKey, ttl: cache.DefaultRedisTTL, owned: false}

	err := c.Set(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error when setting nil ModelCache, got nil")
	}
	if spy.setCalls != 0 {
		t.Errorf("store.Set called %d time(s), want 0 — nil should be rejected before writing", spy.setCalls)
	}
}
