package responsecache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v5"

	"gomodel/internal/cache"
	"gomodel/internal/core"
)

var cacheablePaths = map[string]bool{
	"/v1/chat/completions": true,
	"/v1/responses":        true,
	"/v1/embeddings":       true,
}

type simpleCacheMiddleware struct {
	store cache.Store
	ttl   time.Duration
	wg    sync.WaitGroup
}

func newSimpleCacheMiddleware(store cache.Store, ttl time.Duration) *simpleCacheMiddleware {
	return &simpleCacheMiddleware{store: store, ttl: ttl}
}

func (m *simpleCacheMiddleware) Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if m.store == nil {
				return next(c)
			}
			path := c.Request().URL.Path
			if !cacheablePaths[path] || c.Request().Method != http.MethodPost {
				return next(c)
			}
			if shouldSkipCache(c.Request()) {
				return next(c)
			}
			body, err := io.ReadAll(c.Request().Body)
			if err != nil {
				return err
			}
			c.Request().Body = io.NopCloser(bytes.NewReader(body))
			if isStreamingRequest(path, body) {
				return next(c)
			}
			plan := core.GetExecutionPlan(c.Request().Context())
			if shouldSkipCacheForExecutionPlan(plan) {
				return next(c)
			}
			hit, err := m.TryHit(c, body)
			if err != nil || hit {
				return err
			}
			return m.StoreAfter(c, body, func() error { return next(c) })
		}
	}
}

// TryHit checks the exact-match cache. Returns (true, nil) and writes the cached
// response if found. Returns (false, nil) on a miss.
func (m *simpleCacheMiddleware) TryHit(c *echo.Context, body []byte) (bool, error) {
	if m == nil || m.store == nil {
		return false, nil
	}
	path := c.Request().URL.Path
	plan := core.GetExecutionPlan(c.Request().Context())
	key := hashRequest(path, body, plan)
	cached, err := m.store.Get(c.Request().Context(), key)
	if err != nil {
		return false, nil
	}
	if len(cached) > 0 {
		c.Response().Header().Set("Content-Type", "application/json")
		c.Response().Header().Set("X-Cache", "HIT (exact)")
		c.Response().WriteHeader(http.StatusOK)
		_, _ = c.Response().Write(cached)
		slog.Info("response cache hit (exact)",
			"path", path,
			"request_id", c.Request().Header.Get("X-Request-ID"),
		)
		return true, nil
	}
	return false, nil
}

// StoreAfter calls next, captures the response, and asynchronously stores it on 200 OK.
func (m *simpleCacheMiddleware) StoreAfter(c *echo.Context, body []byte, next func() error) error {
	if m == nil || m.store == nil {
		return next()
	}
	path := c.Request().URL.Path
	plan := core.GetExecutionPlan(c.Request().Context())
	key := hashRequest(path, body, plan)

	capture := &responseCapture{
		ResponseWriter: c.Response(),
		body:           &bytes.Buffer{},
	}
	c.SetResponse(capture)
	if err := next(); err != nil {
		return err
	}
	if capture.status == http.StatusOK && capture.body.Len() > 0 {
		data := bytes.Clone(capture.body.Bytes())
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			storeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := m.store.Set(storeCtx, key, data, m.ttl); err != nil {
				slog.Warn("response cache write failed", "key", key, "err", err)
			}
		}()
	}
	return nil
}

// close waits for all in-flight cache writes to complete, then closes the store.
func (m *simpleCacheMiddleware) close() error {
	m.wg.Wait()
	return m.store.Close()
}

func shouldSkipCacheForExecutionPlan(plan *core.ExecutionPlan) bool {
	return plan != nil && plan.Mode == core.ExecutionModeTranslated && plan.Resolution == nil
}

func shouldSkipCache(req *http.Request) bool {
	cc := req.Header.Get("Cache-Control")
	if cc == "" {
		return false
	}
	directives := strings.Split(strings.ToLower(cc), ",")
	for _, d := range directives {
		d = strings.TrimSpace(d)
		if d == "no-cache" || d == "no-store" {
			return true
		}
	}
	return false
}

func isStreamingRequest(path string, body []byte) bool {
	if path == "/v1/embeddings" {
		return false
	}
	var p struct {
		Stream *bool `json:"stream"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return false
	}
	return p.Stream != nil && *p.Stream
}

func hashRequest(path string, body []byte, plan *core.ExecutionPlan) string {
	h := sha256.New()
	h.Write([]byte(path))
	h.Write([]byte{0})
	if plan != nil {
		h.Write([]byte(plan.Mode))
		h.Write([]byte{0})
		h.Write([]byte(plan.ProviderType))
		h.Write([]byte{0})
		h.Write([]byte(plan.ResolvedQualifiedModel()))
		h.Write([]byte{0})
	}
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

type responseCapture struct {
	http.ResponseWriter
	body   *bytes.Buffer
	status int
}

func (r *responseCapture) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseCapture) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *responseCapture) Write(b []byte) (int, error) {
	// Write to the underlying ResponseWriter first so the client always receives
	// the response. Buffer a copy separately for cache storage only.
	// Note: b originates from upstream LLM API responses (JSON), not from
	// client-controlled input, so there is no XSS risk here.
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	if n > 0 {
		r.body.Write(b[:n])
	}
	return n, err
}
