package responsecache

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/cache"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
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
	handlerCalls := 0

	run := func(veto bool) *httptest.ResponseRecorder {
		t.Helper()
		workflow := &core.Workflow{}
		c, rec := postWithWorkflow(t, "/v1/chat/completions", body, workflow)
		err := m.HandleRequest(c, body, func() error {
			handlerCalls++
			// A plugin phase runs inside the handler and vetoes on the
			// workflow the cache middleware already holds.
			workflow.PluginState = &vetoState{noStore: veto}
			return c.JSON(http.StatusOK, map[string]string{"n": "1"})
		})
		require.NoError(t, err)

		return rec
	}

	rec1 := run(true)
	require.Empty(t, rec1.Header().Get("X-Cache"))

	m.simple.wg.Wait()
	m.semantic.wg.Wait()

	rec2 := run(false)
	require.Empty(t, rec2.Header().Get("X-Cache"))
	require.Equal(t, 2, handlerCalls)

	m.simple.wg.Wait()
	m.semantic.wg.Wait()

	rec3 := run(false)
	require.NotEmpty(t, rec3.Header().Get("X-Cache"))
	require.Equal(t, 2, handlerCalls)
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
		require.NoError(t, err)

		return result
	}
	r := run(true)
	require.Empty(t, r.CacheType)

	m.simple.wg.Wait()
	r = run(false)
	require.Empty(t, r.CacheType)
	require.Equal(t, 2, calls)

	m.simple.wg.Wait()
	r = run(false)
	require.Equal(t, CacheTypeExact, r.CacheType)
	require.Equal(t, 2, calls)
}
