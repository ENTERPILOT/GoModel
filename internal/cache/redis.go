package cache

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	DefaultRedisTTL = 24 * time.Hour
)

// RedisStoreConfig holds configuration for generic Redis key-value store.
type RedisStoreConfig struct {
	URL    string
	Prefix string
	TTL    time.Duration
}

// RedisStore implements generic key-value storage. Used by response cache.
type RedisStore struct {
	client *redis.Client
	prefix string
	ttl    time.Duration
}

// NewRedisStore creates a Redis-based key-value store.
func NewRedisStore(cfg RedisStoreConfig) (*RedisStore, error) {
	opts, err := redis.ParseURL(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid redis URL: %w", err)
	}
	client := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("failed to connect to redis: %w", err)
	}
	ttl := cfg.TTL
	if ttl == 0 {
		ttl = DefaultRedisTTL
	}
	slog.Info("redis store connected", "prefix", cfg.Prefix, "ttl", ttl)
	return &RedisStore{client: client, prefix: cfg.Prefix, ttl: ttl}, nil
}

// Get retrieves value by key.
func (s *RedisStore) Get(ctx context.Context, key string) ([]byte, error) {
	fullKey := s.prefix + key
	data, err := s.client.Get(ctx, fullKey).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, fmt.Errorf("redis get: %w", err)
	}
	return data, nil
}

// Set stores value with TTL.
func (s *RedisStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if ttl == 0 {
		ttl = s.ttl
	}
	fullKey := s.prefix + key
	if err := s.client.Set(ctx, fullKey, value, ttl).Err(); err != nil {
		return fmt.Errorf("redis set: %w", err)
	}
	return nil
}

// Close closes the Redis connection.
func (s *RedisStore) Close() error {
	if s.client != nil {
		return s.client.Close()
	}
	return nil
}
