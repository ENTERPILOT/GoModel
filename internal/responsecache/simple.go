package responsecache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/tidwall/gjson"

	"gomodel/internal/auditlog"
	"gomodel/internal/cache"
	"gomodel/internal/core"
)

var cacheablePaths = map[string]bool{
	"/v1/chat/completions": true,
	"/v1/responses":        true,
	"/v1/embeddings":       true,
}

const (
	cacheWriteWorkerCount = 8
	cacheWriteQueueSize   = 256
)

type cacheWriteJob struct {
	key  string
	data []byte
}

type simpleCacheMiddleware struct {
	store cache.Store
	ttl   time.Duration
	wg    sync.WaitGroup
	jobs  chan cacheWriteJob

	workers sync.WaitGroup
	mu      sync.RWMutex
	closed  bool
}

func newSimpleCacheMiddleware(store cache.Store, ttl time.Duration) *simpleCacheMiddleware {
	m := &simpleCacheMiddleware{
		store: store,
		ttl:   ttl,
		jobs:  make(chan cacheWriteJob, cacheWriteQueueSize),
	}
	m.startWorkers()
	return m
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
			body, cacheable, err := requestBodyForCache(c.Request())
			if err != nil {
				return core.NewInvalidRequestError(err.Error(), err)
			}
			if !cacheable {
				return next(c)
			}
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
		auditlog.EnrichEntryWithCacheType(c, CacheTypeExact)
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
		if core.GetFallbackUsed(c.Request().Context()) {
			return nil
		}
		data := bytes.Clone(capture.body.Bytes())
		m.enqueueWrite(cacheWriteJob{key: key, data: data})
	}
	return nil
}

// close waits for all in-flight cache writes to complete, then closes the store.
func (m *simpleCacheMiddleware) close() error {
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		close(m.jobs)
	}
	m.mu.Unlock()
	m.workers.Wait()
	m.wg.Wait()
	return m.store.Close()
}

func (m *simpleCacheMiddleware) startWorkers() {
	for range cacheWriteWorkerCount {
		m.workers.Go(func() {
			for job := range m.jobs {
				storeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				err := m.store.Set(storeCtx, job.key, job.data, m.ttl)
				cancel()
				if err != nil {
					slog.Warn("response cache write failed", "key", job.key, "err", err)
				}
				m.wg.Done()
			}
		})
	}
}

func (m *simpleCacheMiddleware) enqueueWrite(job cacheWriteJob) {
	m.mu.RLock()
	if m.closed {
		m.mu.RUnlock()
		return
	}
	// Hold the read lock across Add+send so Close cannot observe this write as
	// untracked. If the non-blocking send misses, roll back the Add before
	// releasing the lock and logging the dropped write.
	m.wg.Add(1)
	select {
	case m.jobs <- job:
		m.mu.RUnlock()
	default:
		m.wg.Done()
		m.mu.RUnlock()
		slog.Warn("response cache write queue full", "key", job.key)
	}
}

func shouldSkipCacheForExecutionPlan(plan *core.ExecutionPlan) bool {
	if plan == nil {
		return true
	}
	if !plan.CacheEnabled() {
		return true
	}
	return plan.Mode == core.ExecutionModeTranslated && plan.Resolution == nil
}

func requestBodyForCache(req *http.Request) ([]byte, bool, error) {
	if snapshot := core.GetRequestSnapshot(req.Context()); snapshot != nil {
		if snapshot.BodyNotCaptured {
			return nil, false, nil
		}
		if body := snapshot.CapturedBodyView(); body != nil {
			return body, true, nil
		}
	}
	if req.Body == nil {
		return []byte{}, true, nil
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, false, err
	}
	if body == nil {
		body = []byte{}
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	return body, true, nil
}

func shouldSkipCache(req *http.Request) bool {
	cc := req.Header.Get("Cache-Control")
	if cc == "" {
		return false
	}
	directives := strings.SplitSeq(strings.ToLower(cc), ",")
	for d := range directives {
		d = strings.TrimSpace(d)
		if d == "no-cache" || d == "no-store" {
			return true
		}
	}
	return false
}

func isStreamingRequest(path string, body []byte) bool {
	return isStreamingRequestGJSON(path, body)
}

func isStreamingRequestGJSON(path string, body []byte) bool {
	if path == "/v1/embeddings" {
		return false
	}
	// gjson returns the first matching top-level field. That differs from
	// encoding/json on duplicate keys, but the cache hot path favors the cheaper
	// first-match check because duplicate stream fields are not expected.
	result := gjson.GetBytes(body, "stream")
	if !result.Exists() || (result.Type != gjson.True && result.Type != gjson.False) {
		return false
	}
	return result.Bool()
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
