package cache

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/enterpilot/gomodel/egress"
)

const (
	DefaultRedisTTL = 24 * time.Hour

	// go-redis defaults to 5s socket timeouts plus retries, so a degraded Redis
	// stalls every caller for seconds. A healthy Redis answers in milliseconds;
	// fail fast instead. URL query parameters (dial_timeout, read_timeout,
	// write_timeout) override these.
	defaultRedisDialTimeout = 2 * time.Second
	defaultRedisIOTimeout   = 500 * time.Millisecond

	// redisTimeoutLogInterval limits Get timeout logs to one per interval.
	redisTimeoutLogInterval = time.Minute
)

// RedisStoreConfig holds configuration for generic Redis key-value store.
type RedisStoreConfig struct {
	URL    string
	Prefix string
	TTL    time.Duration
	// GetTimeout bounds each Get, retries included. A Get past it returns an
	// error the caller can treat as a miss. Zero leaves Get bounded by ctx and
	// the client socket timeouts only.
	GetTimeout time.Duration
}

// RedisStore implements generic key-value storage. Used by response cache.
type RedisStore struct {
	client     *redis.Client
	prefix     string
	ttl        time.Duration
	getTimeout time.Duration

	lastTimeoutLog atomic.Int64
}

// NewRedisStore creates a Redis-based key-value store.
func NewRedisStore(cfg RedisStoreConfig) (*RedisStore, error) {
	installRedisLogger()
	opts, err := redisOptions(cfg.URL)
	if err != nil {
		return nil, err
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
	return &RedisStore{client: client, prefix: cfg.Prefix, ttl: ttl, getTimeout: cfg.GetTimeout}, nil
}

// redisOptions parses a Redis URL and applies fail-fast defaults for the
// timeouts the URL leaves unset.
func redisOptions(rawURL string) (*redis.Options, error) {
	opts, err := redis.ParseURL(rawURL)
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
	// ParseURL leaves an unset timeout at 0 and maps an explicit "0" to -1
	// (disabled), so 0 means the URL did not choose one.
	if opts.DialTimeout == 0 {
		opts.DialTimeout = defaultRedisDialTimeout
	}
	if opts.ReadTimeout == 0 {
		opts.ReadTimeout = defaultRedisIOTimeout
	}
	if opts.WriteTimeout == 0 {
		opts.WriteTimeout = defaultRedisIOTimeout
	}
	// Without this go-redis ignores context deadlines and cancellation.
	opts.ContextTimeoutEnabled = true
	opts.Dialer = redisDialer(opts.TLSConfig)
	return opts, nil
}

// Get retrieves value by key.
func (s *RedisStore) Get(ctx context.Context, key string) ([]byte, error) {
	getCtx := ctx
	if s.getTimeout > 0 {
		var cancel context.CancelFunc
		getCtx, cancel = context.WithTimeout(ctx, s.getTimeout)
		defer cancel()
	}
	fullKey := s.prefix + key
	data, err := s.client.Get(getCtx, fullKey).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		if getCtx.Err() != nil && ctx.Err() == nil {
			s.logGetTimeout()
		}
		return nil, fmt.Errorf("redis get: %w", err)
	}
	return data, nil
}

// logGetTimeout logs a Get that ran out of its budget at debug level, at most
// once per redisTimeoutLogInterval so a degraded Redis cannot flood the logs.
func (s *RedisStore) logGetTimeout() {
	now := time.Now().UnixNano()
	last := s.lastTimeoutLog.Load()
	if now-last < int64(redisTimeoutLogInterval) || !s.lastTimeoutLog.CompareAndSwap(last, now) {
		return
	}
	slog.Debug("redis get timed out, treating as a miss", "prefix", s.prefix, "timeout", s.getTimeout)
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
