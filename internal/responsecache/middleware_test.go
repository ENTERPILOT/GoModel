package responsecache

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

	"gomodel/internal/cache"
	"gomodel/internal/core"
)

func TestSimpleCacheMiddleware_CacheHit(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()
	mw := NewResponseCacheMiddlewareWithStore(store, time.Hour)
	e := echo.New()
	e.Use(mw.Middleware())
	e.POST("/v1/chat/completions", func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"result": "cached"})
	})

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first request: got status %d", rec.Code)
	}
	if rec.Header().Get("X-Cache") != "" {
		t.Fatalf("first request should not have X-Cache: %s", rec.Header().Get("X-Cache"))
	}

	// Wait for the tracked background write to complete before the second request.
	mw.inner.wg.Wait()

	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second request: got status %d", rec2.Code)
	}
	if rec2.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("second request should have X-Cache=HIT, got %s", rec2.Header().Get("X-Cache"))
	}
	if !bytes.Contains(rec2.Body.Bytes(), []byte("cached")) {
		t.Fatalf("cached response body missing expected content: %s", rec2.Body.String())
	}
}

func TestSimpleCacheMiddleware_DifferentBodyDifferentKey(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()
	mw := NewResponseCacheMiddlewareWithStore(store, time.Hour)
	e := echo.New()
	e.Use(mw.Middleware())
	e.POST("/v1/chat/completions", func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"msg": c.Request().URL.Path})
	})

	body1 := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	body2 := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"bye"}]}`)

	req1 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body1))
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()
	e.ServeHTTP(rec1, req1)
	if rec1.Header().Get("X-Cache") != "" {
		t.Fatal("first request should miss")
	}

	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)
	if rec2.Header().Get("X-Cache") != "" {
		t.Fatal("different body should miss cache")
	}
}

func TestHashRequest_ResolvedModelChangesKey(t *testing.T) {
	body := []byte(`{"model":"anthropic/claude-opus-4-6","messages":[{"role":"user","content":"hi"}]}`)

	first := hashRequest("/v1/chat/completions", body, &core.RequestModelResolution{
		ResolvedSelector: core.ModelSelector{Provider: "openai", Model: "gpt-5-nano"},
	})
	second := hashRequest("/v1/chat/completions", body, &core.RequestModelResolution{
		ResolvedSelector: core.ModelSelector{Provider: "anthropic", Model: "claude-opus-4-6"},
	})

	if first == second {
		t.Fatal("resolved model should affect cache key")
	}
}

func TestSimpleCacheMiddleware_SkipsStreaming(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()
	mw := NewResponseCacheMiddlewareWithStore(store, time.Hour)
	e := echo.New()
	e.Use(mw.Middleware())
	callCount := 0
	e.POST("/v1/chat/completions", func(c *echo.Context) error {
		callCount++
		return c.JSON(http.StatusOK, map[string]string{"n": "1"})
	})

	body := []byte(`{"model":"gpt-4","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
	}
	if callCount != 2 {
		t.Fatalf("streaming requests should not be cached, handler called %d times", callCount)
	}
}

func TestIsStreamingRequest(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
		want bool
	}{
		{"stream true compact", "/v1/chat/completions", `{"stream":true}`, true},
		{"stream true with spaces", "/v1/chat/completions", `{"stream" : true}`, true},
		{"stream false", "/v1/chat/completions", `{"stream":false}`, false},
		{"stream absent", "/v1/chat/completions", `{"model":"gpt-4"}`, false},
		{"embeddings path always false", "/v1/embeddings", `{"stream":true}`, false},
		{"stream in prompt text not a bool", "/v1/chat/completions", `{"messages":[{"content":"say stream:true please"}]}`, false},
		{"invalid json", "/v1/chat/completions", `not json`, false},
		{"stream null", "/v1/chat/completions", `{"stream":null}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isStreamingRequest(tt.path, []byte(tt.body))
			if got != tt.want {
				t.Errorf("isStreamingRequest(%q, %q) = %v, want %v", tt.path, tt.body, got, tt.want)
			}
		})
	}
}

func TestSimpleCacheMiddleware_SkipsNoCache(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()
	mw := NewResponseCacheMiddlewareWithStore(store, time.Hour)
	e := echo.New()
	e.Use(mw.Middleware())
	callCount := 0
	e.POST("/v1/chat/completions", func(c *echo.Context) error {
		callCount++
		return c.JSON(http.StatusOK, map[string]string{"n": "1"})
	})

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cache-Control", "no-cache")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Cache-Control", "no-cache")
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)
	if callCount != 2 {
		t.Fatalf("no-cache requests should bypass cache, handler called %d times", callCount)
	}
}

func TestSimpleCacheMiddleware_NonCacheablePath(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()
	mw := NewResponseCacheMiddlewareWithStore(store, time.Hour)
	e := echo.New()
	e.Use(mw.Middleware())
	callCount := 0
	e.POST("/v1/models", func(c *echo.Context) error {
		callCount++
		return c.JSON(http.StatusOK, map[string]string{"n": "1"})
	})

	body := []byte(`{}`)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/models", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
	}
	if callCount != 2 {
		t.Fatalf("/v1/models is not cacheable, handler called %d times", callCount)
	}
}

func TestSimpleCacheMiddleware_CloseWaitsForPendingWrites(t *testing.T) {
	store := cache.NewMapStore()
	mw := NewResponseCacheMiddlewareWithStore(store, time.Hour)
	e := echo.New()
	e.Use(mw.Middleware())
	e.POST("/v1/chat/completions", func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"result": "ok"})
	})

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"close-test"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// Close must drain any in-flight write before closing the store.
	// If Close races store.Close against the goroutine's Set, this will
	// panic or produce a data race under -race.
	if err := mw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestSimpleCacheMiddleware_BodyReadErrorPropagated(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()
	mw := NewResponseCacheMiddlewareWithStore(store, time.Hour)
	e := echo.New()
	e.Use(mw.Middleware())
	handlerCalled := false
	e.POST("/v1/chat/completions", func(c *echo.Context) error {
		handlerCalled = true
		return c.JSON(http.StatusOK, map[string]string{"n": "1"})
	})

	readErr := errors.New("simulated body read error")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", io.NopCloser(&errReader{err: readErr}))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if handlerCalled {
		t.Error("downstream handler should not be called when body read fails")
	}
}

// errReader is an io.Reader that always returns an error.
type errReader struct{ err error }

func (r *errReader) Read(_ []byte) (int, error) { return 0, r.err }
