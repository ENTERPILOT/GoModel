package responsecache

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/cache"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/labstack/echo/v5"
)

// vetoState stands in for the plugin request state the runtime attaches to
// the workflow; the cache only sees it as core.ResponseCacheVeto.
type vetoState struct{ noStore bool }

func (v *vetoState) NoStore() bool { return v.noStore }

func TestHandleRequest_PluginNoStoreSkipsCacheWrites(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()

	emb := &mockEmbedder{vector: []float32{1, 0, 0}}
	vecStore := NewMapVecStore()
	semCfg := config.SemanticCacheConfig{
		SimilarityThreshold:     0.90,
		TTL:                     new(3600),
		MaxConversationMessages: new(10),
	}
	m := &ResponseCacheMiddleware{
		simple:   newSimpleCacheMiddleware(store, time.Hour, nil),
		semantic: newSemanticCacheMiddleware(emb, vecStore, semCfg, nil),
	}

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"plugin-no-store"}]}`)
	e := echo.New()
	handlerCalls := 0

	run := func(veto bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		workflow := &core.Workflow{}
		req = req.WithContext(core.WithWorkflow(req.Context(), workflow))
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		if err := m.HandleRequest(c, body, func() error {
			handlerCalls++
			// A plugin phase runs inside the handler and vetoes on the
			// workflow the cache middleware already holds.
			workflow.PluginState = &vetoState{noStore: veto}
			return c.JSON(http.StatusOK, map[string]string{"n": "1"})
		}); err != nil {
			t.Fatalf("HandleRequest: %v", err)
		}
		return rec
	}

	rec1 := run(true)
	if rec1.Header().Get("X-Cache") != "" {
		t.Fatalf("vetoed response should not be a hit, got X-Cache=%q", rec1.Header().Get("X-Cache"))
	}
	m.simple.wg.Wait()
	m.semantic.wg.Wait()

	rec2 := run(false)
	if rec2.Header().Get("X-Cache") != "" {
		t.Fatalf("vetoed response must not populate either cache, got X-Cache=%q", rec2.Header().Get("X-Cache"))
	}
	if handlerCalls != 2 {
		t.Fatalf("expected the second request to run the handler, got %d calls", handlerCalls)
	}
	m.simple.wg.Wait()
	m.semantic.wg.Wait()

	rec3 := run(false)
	if rec3.Header().Get("X-Cache") == "" {
		t.Fatal("a response without a veto must be stored and served on the next request")
	}
	if handlerCalls != 2 {
		t.Fatalf("expected a cache hit on the third request, got %d handler calls", handlerCalls)
	}
}

func TestHandleInternalRequest_PluginNoStoreSkipsCacheWrites(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()
	m := &ResponseCacheMiddleware{simple: newSimpleCacheMiddleware(store, time.Hour, nil)}

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"internal-no-store"}]}`)
	calls := 0
	run := func(veto bool) *InternalHandleResult {
		t.Helper()
		workflow := &core.Workflow{}
		ctx := core.WithWorkflow(context.Background(), workflow)
		result, err := m.HandleInternalRequest(ctx, http.MethodPost, "/v1/chat/completions", body, func(context.Context) (*InternalResponse, error) {
			calls++
			workflow.PluginState = &vetoState{noStore: veto}
			return &InternalResponse{StatusCode: http.StatusOK, ContentType: "application/json", Body: []byte(`{"n":1}`)}, nil
		})
		if err != nil {
			t.Fatalf("HandleInternalRequest: %v", err)
		}
		return result
	}

	if r := run(true); r.CacheType != "" {
		t.Fatalf("first call must miss, got cache type %q", r.CacheType)
	}
	m.simple.wg.Wait()
	if r := run(false); r.CacheType != "" || calls != 2 {
		t.Fatalf("vetoed response must not be stored: cache type %q, calls %d", r.CacheType, calls)
	}
	m.simple.wg.Wait()
	if r := run(false); r.CacheType != CacheTypeExact || calls != 2 {
		t.Fatalf("un-vetoed response must be stored: cache type %q, calls %d", r.CacheType, calls)
	}
}
