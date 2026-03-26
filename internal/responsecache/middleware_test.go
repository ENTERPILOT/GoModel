package responsecache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

	"gomodel/internal/cache"
	"gomodel/internal/core"
)

var benchmarkStreamingBody = []byte(`{"model":"gpt-4","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

type explodingCacheReadCloser struct{}

func (explodingCacheReadCloser) Read([]byte) (int, error) {
	return 0, errors.New("live request body should not be read")
}

func (explodingCacheReadCloser) Close() error {
	return nil
}

type concurrentTrackingStore struct {
	current       atomic.Int32
	maxConcurrent atomic.Int32
	enterCh       chan struct{}
	releaseCh     chan struct{}
}

func newConcurrentTrackingStore() *concurrentTrackingStore {
	return &concurrentTrackingStore{
		enterCh:   make(chan struct{}, 1024),
		releaseCh: make(chan struct{}),
	}
}

func (s *concurrentTrackingStore) Get(context.Context, string) ([]byte, error) {
	return nil, nil
}

func (s *concurrentTrackingStore) Set(_ context.Context, _ string, _ []byte, _ time.Duration) error {
	current := s.current.Add(1)
	for {
		max := s.maxConcurrent.Load()
		if current <= max {
			break
		}
		if s.maxConcurrent.CompareAndSwap(max, current) {
			break
		}
	}
	s.enterCh <- struct{}{}
	<-s.releaseCh
	s.current.Add(-1)
	return nil
}

func (s *concurrentTrackingStore) Close() error {
	return nil
}

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
	mw.simple.wg.Wait()

	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second request: got status %d", rec2.Code)
	}
	if rec2.Header().Get("X-Cache") != "HIT (exact)" {
		t.Fatalf("second request should have X-Cache=HIT (exact), got %s", rec2.Header().Get("X-Cache"))
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

	first := hashRequest("/v1/chat/completions", body, &core.ExecutionPlan{
		Mode: core.ExecutionModeTranslated,
		Resolution: &core.RequestModelResolution{
			ResolvedSelector: core.ModelSelector{Provider: "openai", Model: "gpt-5-nano"},
		},
	})
	second := hashRequest("/v1/chat/completions", body, &core.ExecutionPlan{
		Mode: core.ExecutionModeTranslated,
		Resolution: &core.RequestModelResolution{
			ResolvedSelector: core.ModelSelector{Provider: "anthropic", Model: "claude-opus-4-6"},
		},
	})

	if first == second {
		t.Fatal("resolved model should affect cache key")
	}
}

func TestHashRequest_ModeChangesKey(t *testing.T) {
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)

	first := hashRequest("/v1/chat/completions", body, &core.ExecutionPlan{
		Mode: core.ExecutionModeTranslated,
	})
	second := hashRequest("/v1/chat/completions", body, &core.ExecutionPlan{
		Mode: core.ExecutionModePassthrough,
	})

	if first == second {
		t.Fatal("execution mode should affect cache key")
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
	for range 2 {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
	}
	if callCount != 2 {
		t.Fatalf("streaming requests should not be cached, handler called %d times", callCount)
	}
}

func TestSimpleCacheMiddleware_SkipsPartialTranslatedPlan(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()
	mw := NewResponseCacheMiddlewareWithStore(store, time.Hour)
	e := echo.New()
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			desc := core.DescribeEndpoint(c.Request().Method, c.Request().URL.Path)
			ctx := core.WithExecutionPlan(c.Request().Context(), &core.ExecutionPlan{
				Endpoint:     desc,
				Mode:         core.ExecutionModeTranslated,
				Capabilities: core.CapabilitiesForEndpoint(desc),
			})
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})
	e.Use(mw.Middleware())
	callCount := 0
	e.POST("/v1/chat/completions", func(c *echo.Context) error {
		callCount++
		return c.JSON(http.StatusOK, map[string]string{"n": "1"})
	})

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	for range 2 {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Header().Get("X-Cache") != "" {
			t.Fatalf("partial translated plan should bypass cache, got X-Cache=%q", rec.Header().Get("X-Cache"))
		}
	}

	if callCount != 2 {
		t.Fatalf("partial translated plans should bypass cache, handler called %d times", callCount)
	}
}

func TestSimpleCacheMiddleware_SkipsWhenExecutionPlanDisablesCache(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()
	mw := NewResponseCacheMiddlewareWithStore(store, time.Hour)
	e := echo.New()
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			desc := core.DescribeEndpoint(c.Request().Method, c.Request().URL.Path)
			ctx := core.WithExecutionPlan(c.Request().Context(), &core.ExecutionPlan{
				Endpoint:     desc,
				Mode:         core.ExecutionModeTranslated,
				Capabilities: core.CapabilitiesForEndpoint(desc),
				Resolution: &core.RequestModelResolution{
					ResolvedSelector: core.ModelSelector{Provider: "openai", Model: "gpt-4"},
					ProviderType:     "openai",
				},
				Policy: &core.ResolvedExecutionPolicy{
					VersionID: "plan-cache-off",
					Features: core.ExecutionFeatures{
						Cache:      false,
						Audit:      true,
						Usage:      true,
						Guardrails: true,
					},
				},
			})
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})
	e.Use(mw.Middleware())
	callCount := 0
	e.POST("/v1/chat/completions", func(c *echo.Context) error {
		callCount++
		return c.JSON(http.StatusOK, map[string]string{"n": "1"})
	})

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	for range 2 {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Header().Get("X-Cache") != "" {
			t.Fatalf("cache-disabled plan should bypass cache, got X-Cache=%q", rec.Header().Get("X-Cache"))
		}
	}

	if callCount != 2 {
		t.Fatalf("cache-disabled plan should bypass cache, handler called %d times", callCount)
	}
}

func TestSimpleCacheMiddleware_UsesCapturedSnapshotBodyWithoutReadingLiveBody(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()
	mw := NewResponseCacheMiddlewareWithStore(store, time.Hour)
	e := echo.New()
	e.Use(mw.Middleware())
	callCount := 0
	e.POST("/v1/chat/completions", func(c *echo.Context) error {
		callCount++
		return c.JSON(http.StatusOK, map[string]string{"result": "ok"})
	})

	makeRequest := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.Header.Set("Content-Type", "application/json")
		req.Body = explodingCacheReadCloser{}
		frame := core.NewRequestSnapshot(
			http.MethodPost,
			"/v1/chat/completions",
			nil,
			nil,
			nil,
			"application/json",
			[]byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`),
			false,
			"",
			nil,
		)
		return req.WithContext(core.WithRequestSnapshot(req.Context(), frame))
	}

	rec1 := httptest.NewRecorder()
	e.ServeHTTP(rec1, makeRequest())
	if rec1.Code != http.StatusOK {
		t.Fatalf("first request: got status %d", rec1.Code)
	}
	mw.simple.wg.Wait()

	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, makeRequest())
	if rec2.Code != http.StatusOK {
		t.Fatalf("second request: got status %d", rec2.Code)
	}
	if rec2.Header().Get("X-Cache") != "HIT (exact)" {
		t.Fatalf("expected cache hit from snapshot body, got X-Cache=%q", rec2.Header().Get("X-Cache"))
	}
	if callCount != 1 {
		t.Fatalf("expected second request to hit cache, handler called %d times", callCount)
	}
}

func TestSimpleCacheMiddleware_BypassesCacheWhenBodyWasNotCaptured(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()
	mw := NewResponseCacheMiddlewareWithStore(store, time.Hour)
	e := echo.New()
	e.Use(mw.Middleware())
	callCount := 0
	e.POST("/v1/chat/completions", func(c *echo.Context) error {
		callCount++
		return c.JSON(http.StatusOK, map[string]string{"result": "ok"})
	})

	makeRequest := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.Header.Set("Content-Type", "application/json")
		req.Body = explodingCacheReadCloser{}
		frame := core.NewRequestSnapshot(
			http.MethodPost,
			"/v1/chat/completions",
			nil,
			nil,
			nil,
			"application/json",
			nil,
			true,
			"",
			nil,
		)
		return req.WithContext(core.WithRequestSnapshot(req.Context(), frame))
	}

	for i := range 2 {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, makeRequest())
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: got status %d", i+1, rec.Code)
		}
		if got := rec.Header().Get("X-Cache"); got != "" {
			t.Fatalf("expected uncaptured-body request to bypass cache, got X-Cache=%q", got)
		}
	}

	if callCount != 2 {
		t.Fatalf("expected uncaptured-body requests to bypass cache, handler called %d times", callCount)
	}
}

func TestSimpleCacheMiddleware_BodyReadErrorReturnsGatewayError(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()
	mw := NewResponseCacheMiddlewareWithStore(store, time.Hour)
	e := echo.New()

	handler := mw.Middleware()(func(c *echo.Context) error {
		t.Fatal("handler should not be called when request body read fails")
		return nil
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = explodingCacheReadCloser{}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("handler error = %T, want *core.GatewayError", err)
	}
	if gatewayErr.Type != core.ErrorTypeInvalidRequest {
		t.Fatalf("gateway error type = %q, want %q", gatewayErr.Type, core.ErrorTypeInvalidRequest)
	}
}

func TestRequestBodyForCache_BodyNotCapturedTakesPrecedenceOverEmptySnapshotBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	frame := core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/chat/completions",
		nil,
		nil,
		nil,
		"application/json",
		[]byte{},
		true,
		"",
		nil,
	)
	req = req.WithContext(core.WithRequestSnapshot(req.Context(), frame))

	body, cacheable, err := requestBodyForCache(req)
	if err != nil {
		t.Fatalf("requestBodyForCache() error = %v", err)
	}
	if cacheable {
		t.Fatalf("requestBodyForCache() cacheable = true, want false (body=%q)", body)
	}
	if body != nil {
		t.Fatalf("requestBodyForCache() body = %q, want nil", body)
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
		{"duplicate stream keeps first occurrence", "/v1/chat/completions", `{"stream":false,"stream":true}`, false},
		{"duplicate stream first true stays true", "/v1/chat/completions", `{"stream":true,"stream":false}`, true},
		{"duplicate null stream keeps first value", "/v1/chat/completions", `{"stream":true,"stream":null}`, true},
		{"duplicate invalid stream keeps first value", "/v1/chat/completions", `{"stream":true,"stream":"yes"}`, true},
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

func BenchmarkIsStreamingRequestStdlib(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if !isStreamingRequestStdlib("/v1/chat/completions", benchmarkStreamingBody) {
			b.Fatal("expected streaming request")
		}
	}
}

func BenchmarkIsStreamingRequestGJSON(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if !isStreamingRequestGJSON("/v1/chat/completions", benchmarkStreamingBody) {
			b.Fatal("expected streaming request")
		}
	}
}

func BenchmarkRequestBodyForCacheLiveRead(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(benchmarkStreamingBody))
		body, cacheable, err := requestBodyForCache(req)
		if err != nil {
			b.Fatal(err)
		}
		if !cacheable || len(body) == 0 {
			b.Fatalf("unexpected body result: cacheable=%v len=%d", cacheable, len(body))
		}
	}
}

func BenchmarkRequestBodyForCacheSnapshot(b *testing.B) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	frame := core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/chat/completions",
		nil,
		nil,
		nil,
		"application/json",
		benchmarkStreamingBody,
		false,
		"",
		nil,
	)
	req = req.WithContext(core.WithRequestSnapshot(req.Context(), frame))

	b.ReportAllocs()
	for b.Loop() {
		body, cacheable, err := requestBodyForCache(req)
		if err != nil {
			b.Fatal(err)
		}
		if !cacheable || len(body) == 0 {
			b.Fatalf("unexpected body result: cacheable=%v len=%d", cacheable, len(body))
		}
	}
}

func isStreamingRequestStdlib(path string, body []byte) bool {
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
	for range 2 {
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

func TestSimpleCacheMiddleware_LimitsConcurrentCacheWrites(t *testing.T) {
	store := newConcurrentTrackingStore()
	mw := NewResponseCacheMiddlewareWithStore(store, time.Hour)
	e := echo.New()
	e.Use(mw.Middleware())
	e.POST("/v1/chat/completions", func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"result": "ok"})
	})

	const requestCount = cacheWriteWorkerCount * 2

	var reqWG sync.WaitGroup
	for range requestCount {
		reqWG.Go(func() {
			body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("expected 200, got %d", rec.Code)
			}
		})
	}

	for i := range cacheWriteWorkerCount {
		select {
		case <-store.enterCh:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for cache worker %d", i+1)
		}
	}

	if got := store.maxConcurrent.Load(); got > cacheWriteWorkerCount {
		t.Fatalf("expected at most %d concurrent cache writes, got %d", cacheWriteWorkerCount, got)
	}

	for range requestCount {
		store.releaseCh <- struct{}{}
	}
	reqWG.Wait()
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
