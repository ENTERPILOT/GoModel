package responsecache

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/cache"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/embedding"
	"github.com/enterpilot/gomodel/internal/usage"
)

const responseCachePrefix = "gomodel:response:"

var internalRequestHeaderAllowlist = map[string]struct{}{
	http.CanonicalHeaderKey("Accept"):                     {},
	http.CanonicalHeaderKey("Baggage"):                    {},
	http.CanonicalHeaderKey("Cache-Control"):              {},
	http.CanonicalHeaderKey("Content-Type"):               {},
	http.CanonicalHeaderKey("Request-Id"):                 {},
	http.CanonicalHeaderKey("Traceparent"):                {},
	http.CanonicalHeaderKey("Tracestate"):                 {},
	http.CanonicalHeaderKey("User-Agent"):                 {},
	http.CanonicalHeaderKey("X-Cache-Control"):            {},
	http.CanonicalHeaderKey("X-Cache-Semantic-Threshold"): {},
	http.CanonicalHeaderKey("X-Cache-TTL"):                {},
	http.CanonicalHeaderKey("X-Cache-Type"):               {},
	http.CanonicalHeaderKey("X-Request-ID"):               {},
}

// ResponseCacheMiddleware wraps response cache logic. App and server only see this type.
type ResponseCacheMiddleware struct {
	simple   *simpleCacheMiddleware
	semantic *semanticCacheMiddleware
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

// HandleRequest runs the full dual-layer cache check (exact then semantic) for a
// translated inference request that has already been guardrail-patched.
// body is the final patched request bytes; next is the real LLM call.
// Streaming and non-streaming requests are cached independently. Streaming
// misses persist raw SSE bytes and replay them verbatim on cache hits.
func (m *ResponseCacheMiddleware) HandleRequest(c *echo.Context, body []byte, next func() error) error {
	if m == nil {
		return next()
	}
	return m.handle(&echoExchange{c: c}, body, next)
}

// handle runs the dual-layer cache check against any transport. It contains
// the full cache decision logic; HandleRequest and HandleInternalRequest are
// thin transport adapters over it.
func (m *ResponseCacheMiddleware) handle(ex exchange, body []byte, next func() error) error {
	if shouldSkipAllCacheHeaders(ex.RequestHeader) {
		return next()
	}

	skipExact := strings.EqualFold(ex.RequestHeader("X-Cache-Type"), CacheTypeSemantic)
	skipSemantic := m.semantic == nil || strings.EqualFold(ex.RequestHeader("X-Cache-Type"), CacheTypeExact)

	if !skipExact && m.simple != nil {
		hit, err := m.simple.TryHit(ex, body)
		if err != nil || hit {
			return err
		}
	}

	// innerNext is what actually calls the LLM. When exact caching is active we
	// wrap next inside StoreAfter so both cache layers write on a full miss.
	innerNext := next
	if !skipExact && m.simple != nil {
		innerNext = func() error { return m.simple.StoreAfter(ex, body, next) }
	}

	if !skipSemantic {
		return m.semantic.Handle(ex, body, innerNext)
	}

	return innerNext()
}

// HandleInternalRequest runs the cache for a transport-free internal JSON
// request. Request headers are derived from the originating request snapshot
// (allowlisted), and next executes the LLM call, returning a buffered
// response instead of writing to a socket.
func (m *ResponseCacheMiddleware) HandleInternalRequest(
	ctx context.Context,
	method, path string,
	body []byte,
	next func(ctx context.Context) (*InternalResponse, error),
) (*InternalHandleResult, error) {
	if ctx == nil {
		return nil, core.NewInvalidRequestError("context is required", nil)
	}
	if m == nil {
		slog.Error("response cache: HandleInternalRequest called on nil middleware")
		return nil, core.NewProviderError("", http.StatusInternalServerError, "response cache middleware is not initialized", nil)
	}

	ex := newInternalExchange(ctx, method, path, next)
	err := m.handle(ex, body, ex.runNext)
	if err != nil {
		var gatewayErr *core.GatewayError
		if errors.As(err, &gatewayErr) && gatewayErr != nil {
			return nil, gatewayErr
		}
		return nil, core.NewProviderError("", http.StatusInternalServerError, err.Error(), err)
	}

	return ex.result(), nil
}

// UsesRedis reports whether a Redis-backed exact (simple) cache is configured.
// Only then is the cache a meaningful readiness component worth probing.
func (m *ResponseCacheMiddleware) UsesRedis() bool {
	if m == nil || m.simple == nil {
		return false
	}
	_, ok := m.simple.store.(cache.Pinger)
	return ok
}

// Ping verifies connectivity to the Redis-backed exact cache. It returns nil
// when no Redis cache is configured, so callers should gate on UsesRedis first
// if they need to distinguish "not configured" from "reachable".
func (m *ResponseCacheMiddleware) Ping(ctx context.Context) error {
	if m == nil || m.simple == nil {
		return nil
	}
	pinger, ok := m.simple.store.(cache.Pinger)
	if !ok {
		return nil
	}
	return pinger.Ping(ctx)
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
		for key, values := range snapshot.HeadersView() {
			key = http.CanonicalHeaderKey(key)
			if _, allowed := internalRequestHeaderAllowlist[key]; !allowed {
				continue
			}
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
	if strings.HasPrefix(headerValue, "HIT (") && strings.HasSuffix(headerValue, ")") {
		headerValue = strings.TrimSpace(headerValue[len("HIT (") : len(headerValue)-1])
	}
	switch headerValue {
	case CacheTypeExact:
		return CacheTypeExact
	case CacheTypeSemantic:
		return CacheTypeSemantic
	default:
		return ""
	}
}

// NewResponseCacheMiddlewareWithStore creates middleware with a custom store (for testing).
func NewResponseCacheMiddlewareWithStore(store cache.Store, ttl time.Duration) *ResponseCacheMiddleware {
	return &ResponseCacheMiddleware{
		simple: newSimpleCacheMiddleware(store, ttl, nil),
	}
}
