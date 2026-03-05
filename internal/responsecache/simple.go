package responsecache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"gomodel/internal/cache"
)

var cacheablePaths = map[string]bool{
	"/v1/chat/completions": true,
	"/v1/responses":        true,
	"/v1/embeddings":       true,
}

type simpleCacheMiddleware struct {
	store cache.Store
	ttl   time.Duration
}

func newSimpleCacheMiddleware(store cache.Store, ttl time.Duration) *simpleCacheMiddleware {
	return &simpleCacheMiddleware{store: store, ttl: ttl}
}

func (m *simpleCacheMiddleware) Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
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
				return next(c)
			}
			c.Request().Body = io.NopCloser(bytes.NewReader(body))
			if isStreamingRequest(path, body) {
				return next(c)
			}
			key := hashRequest(path, body)
			ctx := c.Request().Context()
			cached, err := m.store.Get(ctx, key)
			if err != nil {
				return next(c)
			}
			if len(cached) > 0 {
				c.Response().Header().Set("Content-Type", "application/json")
				c.Response().Header().Set("X-Cache", "HIT")
				c.Response().WriteHeader(http.StatusOK)
				_, _ = c.Response().Write(cached)
				return nil
			}
			capture := &responseCapture{
				ResponseWriter: c.Response().Writer,
				body:           &bytes.Buffer{},
			}
			c.Response().Writer = capture
			if err := next(c); err != nil {
				return err
			}
			if c.Response().Status == http.StatusOK && capture.body.Len() > 0 {
				go func() {
					storeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					_ = m.store.Set(storeCtx, key, capture.body.Bytes(), m.ttl)
				}()
			}
			return nil
		}
	}
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
	return bytes.Contains(body, []byte(`"stream":true`)) || bytes.Contains(body, []byte(`"stream": true`))
}

func hashRequest(path string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(path))
	h.Write([]byte{0})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

type responseCapture struct {
	http.ResponseWriter
	body *bytes.Buffer
}

func (r *responseCapture) Write(b []byte) (int, error) {
	r.body.Write(b)
	return r.ResponseWriter.Write(b)
}
