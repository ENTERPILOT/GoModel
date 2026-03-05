package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// RedisModelCacheConfig holds Redis config for model cache. Caller uses RedisStore.
type RedisModelCacheConfig struct {
	URL string
	Key string
	TTL time.Duration
}

// NewRedisModelCache creates a Cache from RedisStore. Model-specific logic lives here.
func NewRedisModelCache(cfg RedisModelCacheConfig) (Cache, error) {
	key := cfg.Key
	if key == "" {
		key = DefaultRedisKey
	}
	ttl := cfg.TTL
	if ttl == 0 {
		ttl = DefaultRedisTTL
	}
	store, err := NewRedisStore(RedisStoreConfig{
		URL:    cfg.URL,
		Prefix: "",
		TTL:    ttl,
	})
	if err != nil {
		return nil, err
	}
	slog.Info("redis model cache connected", "key", key, "ttl", ttl)
	return &redisModelCache{store: store, key: key, ttl: ttl}, nil
}

func NewRedisModelCacheWithStore(store Store, key string, ttl time.Duration) Cache {
	if key == "" {
		key = DefaultRedisKey
	}
	if ttl == 0 {
		ttl = DefaultRedisTTL
	}
	return &redisModelCache{store: store, key: key, ttl: ttl}
}

type redisModelCache struct {
	store Store
	key   string
	ttl   time.Duration
}

func (c *redisModelCache) Get(ctx context.Context) (*ModelCache, error) {
	data, err := c.store.Get(ctx, c.key)
	if err != nil || data == nil {
		return nil, err
	}
	var mc ModelCache
	if err := json.Unmarshal(data, &mc); err != nil {
		return nil, fmt.Errorf("model cache parse: %w", err)
	}
	return &mc, nil
}

func (c *redisModelCache) Set(ctx context.Context, mc *ModelCache) error {
	data, err := json.Marshal(mc)
	if err != nil {
		return fmt.Errorf("model cache marshal: %w", err)
	}
	return c.store.Set(ctx, c.key, data, c.ttl)
}

func (c *redisModelCache) Close() error {
	return c.store.Close()
}
