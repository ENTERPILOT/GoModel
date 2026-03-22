package responsecache

import (
	"log/slog"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"gomodel/config"
	"gomodel/internal/cache"
	"gomodel/internal/embedding"
)

const responseCachePrefix = "gomodel:response:"

// ResponseCacheMiddleware wraps response cache logic. App and server only see this type.
type ResponseCacheMiddleware struct {
	simple   *simpleCacheMiddleware
	semantic *semanticCacheMiddleware
}

// NewResponseCacheMiddleware creates middleware from config.
// If neither simple nor semantic cache is configured, returns a no-op middleware.
// rawProviders is threaded through to NewEmbedder for API-key credential resolution.
func NewResponseCacheMiddleware(cfg config.ResponseCacheConfig, rawProviders map[string]config.RawProviderConfig) (*ResponseCacheMiddleware, error) {
	m := &ResponseCacheMiddleware{}

	if cfg.Simple.Redis != nil && cfg.Simple.Redis.URL != "" {
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
		m.simple = newSimpleCacheMiddleware(store, ttl)
		slog.Info("response cache (simple/exact) enabled", "ttl_seconds", cfg.Simple.Redis.TTL, "prefix", prefix)
	} else {
		slog.Warn("response cache (simple/exact) is disabled; set cache.response.simple.redis.url to enable it")
	}

	sem := cfg.Semantic
	if config.SemanticCacheActive(&sem) {
		emb, err := embedding.NewEmbedder(sem.Embedder, rawProviders)
		if err != nil {
			return nil, err
		}
		vs, err := NewVecStore(sem.VectorStore)
		if err != nil {
			_ = emb.Close()
			return nil, err
		}
		m.semantic = newSemanticCacheMiddleware(emb, vs, sem)
		slog.Info("response cache (semantic) enabled",
			"threshold", sem.SimilarityThreshold,
			"ttl_seconds", sem.TTL,
			"vector_store", sem.VectorStore.Type,
			"embedder", sem.Embedder.Provider,
		)
	}

	return m, nil
}

// Middleware returns the Echo middleware function for the exact-match (simple) cache.
// This is kept for backward compatibility but cache checks are now primarily done
// via Handle() inside the translated inference handlers, after guardrail patching.
func (m *ResponseCacheMiddleware) Middleware() echo.MiddlewareFunc {
	if m.simple != nil {
		return m.simple.Middleware()
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error { return next(c) }
	}
}

// HandleRequest runs the full dual-layer cache check (exact then semantic) for a
// translated inference request that has already been guardrail-patched.
// body is the final patched request bytes; next is the real LLM call.
// Returns true if the request was served from cache.
func (m *ResponseCacheMiddleware) HandleRequest(c *echo.Context, body []byte, next func() error) error {
	if m == nil {
		return next()
	}
	if ShouldSkipAllCache(c.Request()) {
		return next()
	}

	skipExact := ShouldSkipExactCache(c.Request())
	skipSemantic := m.semantic == nil || strings.EqualFold(c.Request().Header.Get("X-Cache-Type"), CacheTypeExact)

	if !skipExact && m.simple != nil {
		hit, err := m.simple.TryHit(c, body)
		if err != nil || hit {
			return err
		}
	}

	// innerNext is what actually calls the LLM. When exact caching is active we
	// wrap next inside StoreAfter so both cache layers write on a full miss.
	innerNext := next
	if !skipExact && m.simple != nil {
		innerNext = func() error { return m.simple.StoreAfter(c, body, next) }
	}

	if !skipSemantic {
		return m.semantic.Handle(c, body, innerNext)
	}

	return innerNext()
}

// Close waits for any in-flight cache writes to complete, then releases cache resources.
func (m *ResponseCacheMiddleware) Close() error {
	if m == nil {
		return nil
	}
	var simErr, semErr error
	if m.simple != nil {
		simErr = m.simple.close()
	}
	if m.semantic != nil {
		semErr = m.semantic.close()
	}
	if simErr != nil {
		return simErr
	}
	return semErr
}

// NewResponseCacheMiddlewareWithStore creates middleware with a custom store (for testing).
func NewResponseCacheMiddlewareWithStore(store cache.Store, ttl time.Duration) *ResponseCacheMiddleware {
	return &ResponseCacheMiddleware{
		simple: newSimpleCacheMiddleware(store, ttl),
	}
}
