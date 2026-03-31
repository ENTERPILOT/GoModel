package responsecache

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

	"gomodel/config"
	"gomodel/internal/cache"
	"gomodel/internal/core"
)

func TestHandleRequest_SemanticMissPopulatesExactCache(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()

	emb := &mockEmbedder{vector: []float32{1, 0, 0}}
	vecStore := NewMapVecStore()
	semCfg := config.SemanticCacheConfig{
		SimilarityThreshold:     0.90,
		TTL:                     3600,
		MaxConversationMessages: 10,
	}

	m := &ResponseCacheMiddleware{
		simple:   newSimpleCacheMiddleware(store, time.Hour),
		semantic: newSemanticCacheMiddleware(emb, vecStore, semCfg),
	}

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"handle-request-exact-backfill"}]}`)
	e := echo.New()

	handlerCalls := 0
	run := func() *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		if err := m.HandleRequest(c, body, func() error {
			handlerCalls++
			return c.JSON(http.StatusOK, map[string]string{"n": "1"})
		}); err != nil {
			t.Fatalf("HandleRequest: %v", err)
		}
		return rec
	}

	rec1 := run()
	if rec1.Header().Get("X-Cache") != "" {
		t.Fatalf("first request should miss exact cache, got X-Cache=%q", rec1.Header().Get("X-Cache"))
	}
	if handlerCalls != 1 {
		t.Fatalf("expected 1 handler invocation after first request, got %d", handlerCalls)
	}

	m.simple.wg.Wait()
	m.semantic.wg.Wait()

	rec2 := run()
	if rec2.Header().Get("X-Cache") != "HIT (exact)" {
		t.Fatalf("second request should be exact hit, got X-Cache=%q", rec2.Header().Get("X-Cache"))
	}
	if handlerCalls != 1 {
		t.Fatalf("exact hit should not call handler again, handlerCalls=%d", handlerCalls)
	}
}

func TestHandleRequest_FallbackUsedSkipsCacheWrites(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()

	emb := &mockEmbedder{vector: []float32{1, 0, 0}}
	vecStore := NewMapVecStore()
	semCfg := config.SemanticCacheConfig{
		SimilarityThreshold:     0.90,
		TTL:                     3600,
		MaxConversationMessages: 10,
	}

	m := &ResponseCacheMiddleware{
		simple:   newSimpleCacheMiddleware(store, time.Hour),
		semantic: newSemanticCacheMiddleware(emb, vecStore, semCfg),
	}

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"fallback-skip-cache"}]}`)
	e := echo.New()
	handlerCalls := 0

	run := func(markFallback bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		if err := m.HandleRequest(c, body, func() error {
			handlerCalls++
			if markFallback {
				c.SetRequest(c.Request().WithContext(core.WithFallbackUsed(c.Request().Context())))
			}
			return c.JSON(http.StatusOK, map[string]string{"n": "1"})
		}); err != nil {
			t.Fatalf("HandleRequest: %v", err)
		}
		return rec
	}

	rec1 := run(true)
	if rec1.Header().Get("X-Cache") != "" {
		t.Fatalf("fallback-served response should not be cached, got X-Cache=%q", rec1.Header().Get("X-Cache"))
	}
	if handlerCalls != 1 {
		t.Fatalf("expected 1 handler invocation after first request, got %d", handlerCalls)
	}

	m.simple.wg.Wait()
	m.semantic.wg.Wait()

	rec2 := run(false)
	if rec2.Header().Get("X-Cache") != "" {
		t.Fatalf("fallback-served response should not populate cache, got X-Cache=%q", rec2.Header().Get("X-Cache"))
	}
	if handlerCalls != 2 {
		t.Fatalf("expected second request to execute handler again, got %d calls", handlerCalls)
	}
}
