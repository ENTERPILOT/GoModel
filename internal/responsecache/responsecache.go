package responsecache

import (
	"log/slog"
	"time"

	"github.com/labstack/echo/v5"

	"gomodel/config"
	"gomodel/internal/cache"
)

const responseCachePrefix = "gomodel:response:"

// ResponseCacheMiddleware wraps response cache logic. App and server only see this type.
type ResponseCacheMiddleware struct {
	inner *simpleCacheMiddleware
}

// NewResponseCacheMiddleware creates middleware from config. If simple cache is not configured,
// returns a no-op middleware. All inner logic (simple, redis, etc.) is encapsulated.
func NewResponseCacheMiddleware(cfg config.ResponseCacheConfig) (*ResponseCacheMiddleware, error) {
	if cfg.Simple.Redis == nil || cfg.Simple.Redis.URL == "" {
		slog.Warn("response cache is disabled; set cache.response.simple.redis.url to enable it")
		return &ResponseCacheMiddleware{}, nil
	}
	ttl := time.Duration(cfg.Simple.Redis.TTL) * time.Second
	if ttl == 0 {
		ttl = time.Hour
	}
	prefix := cfg.Simple.Redis.Key
	if prefix == "" {
		prefix = responseCachePrefix
	}
	store, err := cache.NewRedisStore(cache.RedisStoreConfig{
		URL:    cfg.Simple.Redis.URL,
		Prefix: prefix,
		TTL:    ttl,
	})
	if err != nil {
		return nil, err
	}
	slog.Info("response cache enabled", "ttl_seconds", cfg.Simple.Redis.TTL, "prefix", prefix)
	return &ResponseCacheMiddleware{
		inner: newSimpleCacheMiddleware(store, ttl),
	}, nil
}

// Middleware returns the Echo middleware function.
func (m *ResponseCacheMiddleware) Middleware() echo.MiddlewareFunc {
	if m.inner != nil {
		return m.inner.Middleware()
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error { return next(c) }
	}
}

// Close waits for any in-flight cache writes to complete, then releases cache resources.
func (m *ResponseCacheMiddleware) Close() error {
	if m != nil && m.inner != nil && m.inner.store != nil {
		return m.inner.close()
	}
	return nil
}

// NewResponseCacheMiddlewareWithStore creates middleware with a custom store (for testing).
func NewResponseCacheMiddlewareWithStore(store cache.Store, ttl time.Duration) *ResponseCacheMiddleware {
	return &ResponseCacheMiddleware{
		inner: newSimpleCacheMiddleware(store, ttl),
	}
}
