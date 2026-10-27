package cache

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/enterpilot/gomodel/egress"
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
	installRedisLogger()
	opts, err := redis.ParseURL(cfg.URL)
	if err != nil {
		// url.Error quotes the whole URL, and an EscapeError the bad escape;
		// either can hold the password.
		if urlErr, ok := errors.AsType[*url.Error](err); ok {
			err = urlErr.Err
			if _, ok := errors.AsType[url.EscapeError](err); ok {
				err = errors.New("invalid percent-escape")
			}
		}
		return nil, fmt.Errorf("invalid redis URL: %w", err)
	}
	opts.Dialer = redisDialer(opts.TLSConfig)
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
		if errors.Is(err, redis.Nil) {
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

// Ping verifies connectivity to Redis.
func (s *RedisStore) Ping(ctx context.Context) error {
	if s.client == nil {
		return fmt.Errorf("redis client is not initialized")
	}
	return s.client.Ping(ctx).Err()
}

// Close closes the Redis connection.
func (s *RedisStore) Close() error {
	if s.client != nil {
		return s.client.Close()
	}
	return nil
}

// A custom go-redis dialer owns the TLS handshake as well as the TCP connection.
// Keep the egress hook on the raw connection without losing rediss verification.
func redisDialer(tlsConfig *tls.Config) egress.DialFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := egress.DialContext(ctx, network, address)
		if err != nil || tlsConfig == nil {
			return conn, err
		}
		config := tlsConfig.Clone()
		if config.ServerName == "" {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				_ = conn.Close()
				return nil, err
			}
			config.ServerName = host
		}
		tlsConn := tls.Client(conn, config)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return tlsConn, nil
	}
}
