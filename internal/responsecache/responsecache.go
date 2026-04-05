package responsecache

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"gomodel/config"
	"gomodel/internal/cache"
	"gomodel/internal/core"
	"gomodel/internal/embedding"
	"gomodel/internal/usage"
)

const responseCachePrefix = "gomodel:response:"

// ResponseCacheMiddleware wraps response cache logic. App and server only see this type.
type ResponseCacheMiddleware struct {
	simple   *simpleCacheMiddleware
	semantic *semanticCacheMiddleware
	echo     *echo.Echo
}

// InternalHandleResult is the buffered result of running the cache middleware
// for a transport-free internal JSON request.
type InternalHandleResult struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
	CacheType  string
}

// NewResponseCacheMiddleware creates middleware from config.
// If neither simple nor semantic cache is configured, returns a no-op middleware.
// resolvedProviders must be the credential/env-resolved map from providers.InitResult
// (same keys as live routing). Semantic embedder.provider names a key in this map.
func NewResponseCacheMiddleware(
	cfg config.ResponseCacheConfig,
	resolvedProviders map[string]config.RawProviderConfig,
	usageLogger usage.LoggerInterface,
	pricingResolver usage.PricingResolver,
) (*ResponseCacheMiddleware, error) {
	m := &ResponseCacheMiddleware{}
	m.echo = echo.New()
	hitRecorder := newUsageHitRecorder(usageLogger, pricingResolver)

	switch {
	case cfg.Simple == nil:
	case !config.SimpleCacheEnabled(cfg.Simple):
		slog.Info("response cache (simple/exact) disabled by config")
	case cfg.Simple.Redis == nil || cfg.Simple.Redis.URL == "":
		slog.Warn("response cache (simple/exact) enabled in config but redis URL is missing; set cache.response.simple.redis.url or REDIS_URL")
	default:
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
		m.simple = newSimpleCacheMiddleware(store, ttl, hitRecorder)
		slog.Info("response cache (simple/exact) enabled", "ttl_seconds", cfg.Simple.Redis.TTL, "prefix", prefix)
	}

	sem := cfg.Semantic
	if sem != nil && config.SemanticCacheActive(sem) {
		emb, err := embedding.NewEmbedder(sem.Embedder, resolvedProviders)
		if err != nil {
			if m.simple != nil {
				_ = m.simple.close()
			}
			return nil, err
		}
		vs, err := NewVecStore(sem.VectorStore)
		if err != nil {
			_ = emb.Close()
			if m.simple != nil {
				_ = m.simple.close()
			}
			return nil, err
		}
		m.semantic = newSemanticCacheMiddleware(emb, vs, *sem, hitRecorder)
		ttlLog := 0
		if sem.TTL != nil {
			ttlLog = *sem.TTL
		}
		slog.Info("response cache (semantic) enabled",
			"threshold", sem.SimilarityThreshold,
			"ttl_seconds", ttlLog,
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
// Streaming and non-streaming requests are cached independently. Streaming
// misses persist raw SSE bytes and replay them verbatim on cache hits.
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

// HandleInternalRequest runs the normal cache middleware for a transport-free
// internal request using a minimal synthetic HTTP/Echo context.
func (m *ResponseCacheMiddleware) HandleInternalRequest(
	ctx context.Context,
	method, path string,
	body []byte,
	next func(*echo.Context) error,
) (*InternalHandleResult, error) {
	if ctx == nil {
		return nil, core.NewInvalidRequestError("context is required", nil)
	}

	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header = internalRequestHeaders(ctx)
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	var e *echo.Echo
	switch {
	case m == nil:
		e = echo.New()
	case m.echo == nil:
		slog.Error("response cache: HandleInternalRequest called with uninitialized Echo instance")
		return nil, core.NewProviderError("", http.StatusInternalServerError, "response cache middleware is not initialized", nil)
	default:
		e = m.echo
	}
	c := e.NewContext(req, rec)

	var err error
	if m == nil {
		err = next(c)
	} else {
		err = m.HandleRequest(c, body, func() error { return next(c) })
	}
	if err != nil {
		return nil, err
	}

	return &InternalHandleResult{
		StatusCode: rec.Code,
		Headers:    rec.Header().Clone(),
		Body:       bytes.Clone(rec.Body.Bytes()),
		CacheType:  internalCacheType(rec.Header().Get("X-Cache")),
	}, nil
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

func internalRequestHeaders(ctx context.Context) http.Header {
	headers := make(http.Header)
	if snapshot := core.GetRequestSnapshot(ctx); snapshot != nil {
		for key, values := range snapshot.GetHeaders() {
			for _, value := range values {
				headers.Add(key, value)
			}
		}
	}
	if headers.Get("Content-Type") == "" {
		headers.Set("Content-Type", "application/json")
	}
	if requestID := strings.TrimSpace(core.GetRequestID(ctx)); requestID != "" && headers.Get("X-Request-ID") == "" {
		headers.Set("X-Request-ID", requestID)
	}
	return headers
}

func internalCacheType(headerValue string) string {
	headerValue = strings.TrimSpace(headerValue)
	switch headerValue {
	case CacheHeaderExact:
		return CacheTypeExact
	case CacheHeaderSemantic:
		return CacheTypeSemantic
	default:
		return ""
	}
}

// NewResponseCacheMiddlewareWithStore creates middleware with a custom store (for testing).
func NewResponseCacheMiddlewareWithStore(store cache.Store, ttl time.Duration) *ResponseCacheMiddleware {
	return &ResponseCacheMiddleware{
		simple: newSimpleCacheMiddleware(store, ttl, nil),
		echo:   echo.New(),
	}
}
