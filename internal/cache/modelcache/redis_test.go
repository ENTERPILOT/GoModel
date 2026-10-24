package modelcache

import (
	"context"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// NewRedisModelCacheWithStore creates a Cache from an existing Store, letting
// tests exercise redisModelCache without a real Redis connection.
func NewRedisModelCacheWithStore(store cache.Store, key string, ttl time.Duration) Cache {
	if key == "" {
		key = DefaultRedisKey
	}
	if ttl == 0 {
		ttl = cache.DefaultRedisTTL
	}
	return &redisModelCache{store: store, key: key, ttl: ttl, owned: false}
}

func TestRedisModelCache_GetSet(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()
	c := NewRedisModelCacheWithStore(store, "test:models", time.Hour)
	defer c.Close()

	ctx := context.Background()
	got, err := c.Get(ctx)
	require.NoError(t, err)
	require.Nil(t, got)

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
	err = c.Set(ctx, mc)
	require.NoError(t, err)

	got, err = c.Get(ctx)
	require.NoError(t, err)

	require.NotNil(t, got, "expected non-nil ModelCache")
	require.Len(t, got.Providers, 1)

	p, ok := got.Providers["openai"]
	require.True(t, ok)
	assert.Equal(t, "openai", p.ProviderType)
	require.Len(t, p.Models, 1)
	assert.Equal(t, "gpt-4", p.Models[0].ID)
}

func TestRedisModelCache_DefaultKeyAndTTL(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()
	c := NewRedisModelCacheWithStore(store, "", 0)
	defer c.Close()

	rc, ok := c.(*redisModelCache)
	require.True(t, ok)
	assert.Equal(t, DefaultRedisKey, rc.key)
	assert.Equal(t, cache.DefaultRedisTTL, rc.ttl)

	ctx := context.Background()
	mc := &ModelCache{
		UpdatedAt: time.Now(),
		Providers: map[string]CachedProvider{},
	}
	err := c.Set(ctx, mc)
	require.NoError(t, err)

	got, err := c.Get(ctx)
	require.NoError(t, err)
	require.NotNil(t, got)
}

func TestRedisModelCacheWithStore_CloseDoesNotCloseSharedStore(t *testing.T) {
	spy := &spyStore{}
	c := NewRedisModelCacheWithStore(spy, "test:models", time.Hour)
	err := c.Close()
	require.NoError(t, err)
	assert.Equal(t, 0, spy.closeCalls)
}

func TestRedisModelCache_CloseClosesOwnedStore(t *testing.T) {
	spy := &spyStore{}
	c := &redisModelCache{store: spy, key: DefaultRedisKey, ttl: cache.DefaultRedisTTL, owned: true}
	err := c.Close()
	require.NoError(t, err)
	assert.Equal(t, 1, spy.closeCalls)
	err = // Second Close must not panic or error.
		c.Close()
	require.NoError(t, err)
	assert.Equal(t, 2, spy.closeCalls)
}

// spyStore is a cache.Store that records how many times Close and Set have been called.
type spyStore struct {
	closeCalls int
	setCalls   int
}

func (s *spyStore) Get(_ context.Context, _ string) ([]byte, error) { return nil, nil }
func (s *spyStore) Set(_ context.Context, _ string, _ []byte, _ time.Duration) error {
	s.setCalls++
	return nil
}
func (s *spyStore) Close() error { s.closeCalls++; return nil }

func TestRedisModelCache_SetNilReturnsError(t *testing.T) {
	spy := &spyStore{}
	c := &redisModelCache{store: spy, key: DefaultRedisKey, ttl: cache.DefaultRedisTTL, owned: false}

	err := c.Set(context.Background(), nil)
	require.Error(t, err)
	assert.Equal(t, 0, spy.setCalls)
}
