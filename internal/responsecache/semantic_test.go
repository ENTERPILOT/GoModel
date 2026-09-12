package responsecache

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
)

// mockEmbedder is an Embedder implementation for testing that returns a fixed vector.
type mockEmbedder struct {
	vector []float32
	err    error
	calls  int
}

func (m *mockEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	m.calls++
	return m.vector, m.err
}

func (m *mockEmbedder) Close() error { return nil }

func (m *mockEmbedder) Identity() string { return "test\x00mock-model" }

func newTestSemanticMiddleware(threshold float64, maxConvMessages int, excludeSystem bool) (*semanticCacheMiddleware, *MapVecStore, *mockEmbedder) {
	store := NewMapVecStore()
	emb := &mockEmbedder{vector: []float32{1, 0, 0}}
	cfg := config.SemanticCacheConfig{
		SimilarityThreshold:     threshold,
		TTL:                     new(3600),
		MaxConversationMessages: new(maxConvMessages),
		ExcludeSystemPrompt:     excludeSystem,
	}
	m := newSemanticCacheMiddleware(emb, store, cfg, nil)
	return m, store, emb
}

func serveSemanticRequest(t *testing.T, m *semanticCacheMiddleware, body []byte, guardrailsHash string) *httptest.ResponseRecorder {
	t.Helper()
	c, rec := echotest.Post(t, "/v1/chat/completions", body)
	if guardrailsHash != "" {
		req := c.Request()
		c.SetRequest(req.WithContext(core.WithGuardrailsHash(req.Context(), guardrailsHash)))
	}
	err := m.Handle(&echoExchange{c: c}, body, func() error {
		return c.JSON(http.StatusOK, map[string]string{"answer": "42"})
	})
	require.NoError(t, err)

	return rec
}

func TestSemanticCacheMiddleware_CacheHit(t *testing.T) {
	m, store, _ := newTestSemanticMiddleware(0.90, 10, false)
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"What is 2+2?"}]}`)

	rec1 := serveSemanticRequest(t, m, body, "")
	require.Empty(t, rec1.Header().Get("X-Cache"))

	m.wg.Wait()
	require.Equal(t, 1, store.Len())

	rec2 := serveSemanticRequest(t, m, body, "")
	require.Equal(t, "HIT (semantic)", rec2.Header().Get("X-Cache"))
}

func TestSemanticCacheMiddleware_ParaphraseFinalUserSharesNamespace(t *testing.T) {
	m, store, emb := newTestSemanticMiddleware(0.90, 10, false)
	emb.vector = []float32{1, 0, 0}

	body1 := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"What is 2+2?"}]}`)
	body2 := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"What's two plus two?"}]}`)

	serveSemanticRequest(t, m, body1, "")
	m.wg.Wait()
	require.Equal(t, 1, store.Len())

	rec := serveSemanticRequest(t, m, body2, "")
	require.Equal(t, "HIT (semantic)", rec.Header().Get("X-Cache"))
}

func TestSemanticCacheMiddleware_MultiTurnParaphraseLastUserHit(t *testing.T) {
	m, store, emb := newTestSemanticMiddleware(0.90, 10, false)
	emb.vector = []float32{1, 0, 0}

	body1 := []byte(`{"model":"gpt-4","messages":[
		{"role":"user","content":"Remember the number 7."},
		{"role":"assistant","content":"OK."},
		{"role":"user","content":"What is 2+2?"}
	]}`)
	body2 := []byte(`{"model":"gpt-4","messages":[
		{"role":"user","content":"Remember the number 7."},
		{"role":"assistant","content":"OK."},
		{"role":"user","content":"What's two plus two?"}
	]}`)

	serveSemanticRequest(t, m, body1, "")
	m.wg.Wait()
	require.Equal(t, 1, store.Len())

	rec := serveSemanticRequest(t, m, body2, "")
	require.Equal(t, "HIT (semantic)", rec.Header().Get("X-Cache"))
}

func TestSemanticCacheMiddleware_MultimodalAttachmentIsolatesNamespace(t *testing.T) {
	m, store, emb := newTestSemanticMiddleware(0.90, 10, false)
	emb.vector = []float32{1, 0, 0}

	body1 := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":[
		{"type":"text","text":"describe the image"},
		{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}
	]}]}`)
	body2 := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":[
		{"type":"text","text":"describe the image"},
		{"type":"image_url","image_url":{"url":"https://example.com/b.png"}}
	]}]}`)

	serveSemanticRequest(t, m, body1, "")
	m.wg.Wait()
	require.Equal(t, 1, store.Len())

	rec := serveSemanticRequest(t, m, body2, "")
	require.NotEqual(t, "HIT (semantic)", rec.Header().Get("X-Cache"))
}

func TestSemanticCacheMiddleware_CacheMissOnLowScore(t *testing.T) {
	store := NewMapVecStore()
	emb := &mockEmbedder{}

	m := newSemanticCacheMiddleware(emb, store, config.SemanticCacheConfig{
		SimilarityThreshold:     0.99,
		TTL:                     new(3600),
		MaxConversationMessages: new(10),
	}, nil)

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`)

	emb.vector = []float32{1, 0, 0}
	serveSemanticRequest(t, m, body, "")
	m.wg.Wait()

	emb.vector = []float32{0, 1, 0}
	rec := serveSemanticRequest(t, m, body, "")
	require.NotEqual(t, "HIT (semantic)", rec.Header().Get("X-Cache"))
}

func TestSemanticCacheMiddleware_ParamsHashIsolation_Temperature(t *testing.T) {
	m, store, emb := newTestSemanticMiddleware(0.90, 10, false)
	emb.vector = []float32{1, 0, 0}

	temp1 := 0.5
	temp2 := 1.0
	body1, _ := json.Marshal(map[string]any{
		"model":       "gpt-4",
		"temperature": temp1,
		"messages":    []map[string]string{{"role": "user", "content": "same prompt"}},
	})
	body2, _ := json.Marshal(map[string]any{
		"model":       "gpt-4",
		"temperature": temp2,
		"messages":    []map[string]string{{"role": "user", "content": "same prompt"}},
	})

	serveSemanticRequest(t, m, body1, "")
	m.wg.Wait()
	require.Equal(t, 1, store.Len())

	rec := serveSemanticRequest(t, m, body2, "")
	require.NotEqual(t, "HIT (semantic)", rec.Header().Get("X-Cache"))
}

func TestComputeParamsHash_StreamIncludeUsageChangesHash(t *testing.T) {
	base := []byte(`{"model":"gpt-4","stream":true,"messages":[{"role":"user","content":"same prompt"}]}`)
	withUsage := []byte(`{"model":"gpt-4","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"same prompt"}]}`)
	plan := &core.Workflow{
		Mode:         core.ExecutionModeTranslated,
		ProviderType: "openai",
		Resolution: &core.RequestModelResolution{
			ResolvedSelector: core.ModelSelector{Provider: "openai", Model: "gpt-4"},
		},
	}

	first := computeParamsHash(base, "/v1/chat/completions", plan, "", "")
	second := computeParamsHash(withUsage, "/v1/chat/completions", plan, "", "")

	require.NotEqual(t, second, first)
}

func TestComputeParamsHash_StreamModeChangesHash(t *testing.T) {
	base := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"same prompt"}]}`)
	streaming := []byte(`{"model":"gpt-4","stream":true,"messages":[{"role":"user","content":"same prompt"}]}`)
	plan := &core.Workflow{
		Mode:         core.ExecutionModeTranslated,
		ProviderType: "openai",
		Resolution: &core.RequestModelResolution{
			ResolvedSelector: core.ModelSelector{Provider: "openai", Model: "gpt-4"},
		},
	}

	first := computeParamsHash(base, "/v1/chat/completions", plan, "", "")
	second := computeParamsHash(streaming, "/v1/chat/completions", plan, "", "")

	require.NotEqual(t, second, first)
}

func TestSemanticCacheMiddleware_GuardrailsHashIsolation(t *testing.T) {
	m, store, emb := newTestSemanticMiddleware(0.90, 10, false)
	emb.vector = []float32{1, 0, 0}

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"medical question"}]}`)

	serveSemanticRequest(t, m, body, "guardrails-v1-hash")
	m.wg.Wait()
	require.Equal(t, 1, store.Len())

	rec := serveSemanticRequest(t, m, body, "guardrails-v2-hash")
	require.NotEqual(t, "HIT (semantic)", rec.Header().Get("X-Cache"))
}

func TestSemanticCacheMiddleware_DynamicGuardrailsIsolation(t *testing.T) {
	m, store, emb := newTestSemanticMiddleware(0.90, 10, false)
	emb.vector = []float32{1, 0, 0}

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"user PII data"}]}`)

	serveSemanticRequest(t, m, body, "hash-user-a-pii-rule")
	m.wg.Wait()
	require.Equal(t, 1, store.Len())

	rec := serveSemanticRequest(t, m, body, "hash-user-b-different-pii-rule")
	require.NotEqual(t, "HIT (semantic)", rec.Header().Get("X-Cache"))
}

func TestSemanticCacheMiddleware_ConversationThreshold_Skipped(t *testing.T) {
	m, store, emb := newTestSemanticMiddleware(0.90, 2, false)
	emb.vector = []float32{1, 0, 0}

	longConvBody := []byte(`{"model":"gpt-4","messages":[
		{"role":"user","content":"msg1"},
		{"role":"assistant","content":"resp1"},
		{"role":"user","content":"msg2"},
		{"role":"assistant","content":"resp2"},
		{"role":"user","content":"msg3"}
	]}`)

	serveSemanticRequest(t, m, longConvBody, "")
	m.wg.Wait()

	require.Equal(t, 0, store.Len())
}

func TestSemanticCacheMiddleware_ExcludeSystemPrompt(t *testing.T) {
	m, store, emb := newTestSemanticMiddleware(0.90, 10, true)
	emb.vector = []float32{1, 0, 0}

	body := []byte(`{"model":"gpt-4","messages":[
		{"role":"system","content":"You are a helpful assistant."},
		{"role":"user","content":"what is 2+2"}
	]}`)

	serveSemanticRequest(t, m, body, "")
	m.wg.Wait()
	require.Equal(t, 1, store.Len())

	rec := serveSemanticRequest(t, m, body, "")
	require.Equal(t, "HIT (semantic)", rec.Header().Get("X-Cache"))
}

func TestSemanticCacheMiddleware_StreamingMissPopulatesStreamingSemanticCacheOnly(t *testing.T) {
	m, store, _ := newTestSemanticMiddleware(0.90, 10, false)
	streamBody := []byte(`{"model":"gpt-4","stream":true,"messages":[{"role":"user","content":"semantic-stream-cache"}]}`)
	jsonBody := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"semantic-stream-cache"}]}`)
	rawStream := []byte(
		"data: {\"id\":\"chatcmpl-semantic-stream\",\"object\":\"chat.completion.chunk\",\"created\":1234567890,\"model\":\"gpt-4\",\"provider\":\"openai\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Semantic\"},\"finish_reason\":null}]}\n\n" +
			"data: {\"id\":\"chatcmpl-semantic-stream\",\"object\":\"chat.completion.chunk\",\"created\":1234567890,\"model\":\"gpt-4\",\"provider\":\"openai\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" cache\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":2,\"total_tokens\":9}}\n\n" +
			"data: [DONE]\n\n",
	)
	handlerCalls := 0

	run := func(body []byte) *httptest.ResponseRecorder {
		t.Helper()
		c, rec := echotest.Post(t, "/v1/chat/completions", body)
		err := m.Handle(&echoExchange{c: c}, body, func() error {
			handlerCalls++
			if isStreamingRequest(c.Request().URL.Path, body) {
				c.Response().Header().Set("Content-Type", "text/event-stream")
				c.Response().WriteHeader(http.StatusOK)
				_, _ = c.Response().Write(rawStream)
				return nil
			}
			return c.JSON(http.StatusOK, map[string]string{"mode": "json"})
		})
		require.NoError(t, err)

		return rec
	}

	rec1 := run(streamBody)
	require.Empty(t, rec1.Header().Get("X-Cache"))

	m.wg.Wait()

	require.Equal(t, 1, store.Len())

	rec2 := run(jsonBody)
	require.Empty(t, rec2.Header().Get("X-Cache"))
	require.Equal(t, "application/json", rec2.Header().Get("Content-Type"))
	require.Contains(t, rec2.Body.String(), `"mode":"json"`, "non-streaming miss body = %q, want JSON response", rec2.Body.String())
	require.Equal(t, 2, handlerCalls)

	m.wg.Wait()

	require.Equal(t, 2, store.Len())

	rec3 := run(streamBody)
	require.Equal(t, "HIT (semantic)", rec3.Header().Get("X-Cache"))
	require.Equal(t, "text/event-stream", rec3.Header().Get("Content-Type"))
	require.Equal(t, rawStream, rec3.Body.Bytes())
	require.Equal(t, 2, handlerCalls)

	rec4 := run(jsonBody)
	require.Equal(t, "HIT (semantic)", rec4.Header().Get("X-Cache"))
	require.Equal(t, "application/json", rec4.Header().Get("Content-Type"))
	require.Contains(t, rec4.Body.String(), `"mode":"json"`, "non-streaming semantic hit body = %q, want cached JSON response", rec4.Body.String())
	require.Equal(t, 2, handlerCalls)
}

func TestSemanticCacheMiddleware_InvalidStreamingBodySkipsSemanticCacheWrite(t *testing.T) {
	m, store, _ := newTestSemanticMiddleware(0.90, 10, false)
	body := []byte(`{"model":"gpt-4","stream":true,"messages":[{"role":"user","content":"semantic-invalid-stream"}]}`)
	invalidStream := []byte(
		": keep-alive\n\n" +
			"data: {\"id\":\"chatcmpl-semantic-invalid\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n",
	)
	handlerCalls := 0

	run := func() *httptest.ResponseRecorder {
		t.Helper()
		c, rec := echotest.Post(t, "/v1/chat/completions", body)
		err := m.Handle(&echoExchange{c: c}, body, func() error {
			handlerCalls++
			c.Response().Header().Set("Content-Type", "text/event-stream")
			c.Response().WriteHeader(http.StatusOK)
			_, _ = c.Response().Write(invalidStream)
			return nil
		})
		require.NoError(t, err)

		return rec
	}

	rec1 := run()
	require.Empty(t, rec1.Header().Get("X-Cache"))

	m.wg.Wait()

	require.Equal(t, 0, store.Len())

	rec2 := run()
	require.Empty(t, rec2.Header().Get("X-Cache"))
	require.Equal(t, 2, handlerCalls)
}

func TestSemanticCacheMiddleware_NoCacheControlSkip(t *testing.T) {
	m, store, _ := newTestSemanticMiddleware(0.90, 10, false)
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"no-store test"}]}`)
	c, _ := echotest.Post(t, "/v1/chat/completions", body, echotest.WithHeader("Cache-Control", "no-store"))

	handlerCalled := false
	err := m.Handle(&echoExchange{c: c}, body, func() error {
		handlerCalled = true
		return c.JSON(http.StatusOK, map[string]string{"r": "1"})
	})
	require.NoError(t, err)

	m.wg.Wait()

	require.Equal(t, 0, store.Len())
	require.True(t, handlerCalled)
}

func TestSemanticCacheMiddleware_HeaderThresholdOverride(t *testing.T) {
	store := NewMapVecStore()
	emb := &mockEmbedder{}

	m := newSemanticCacheMiddleware(emb, store, config.SemanticCacheConfig{
		SimilarityThreshold:     0.99,
		TTL:                     new(3600),
		MaxConversationMessages: new(10),
	}, nil)

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`)

	emb.vector = []float32{1, 0, 0}
	serveSemanticRequest(t, m, body, "")
	m.wg.Wait()

	emb.vector = []float32{0.95, 0.05, 0}

	c, rec := echotest.Post(t, "/v1/chat/completions", body, echotest.WithHeader("X-Cache-Semantic-Threshold", "0.50"))
	err := m.Handle(&echoExchange{c: c}, body, func() error {
		return c.JSON(http.StatusOK, map[string]string{"r": "1"})
	})
	require.NoError(t, err)
	require.Equal(t, "HIT (semantic)", rec.Header().Get("X-Cache"))
}

func TestSemanticCacheMiddleware_TTLExpiry(t *testing.T) {
	store := NewMapVecStore()
	emb := &mockEmbedder{vector: []float32{1, 0, 0}}

	m := newSemanticCacheMiddleware(emb, store, config.SemanticCacheConfig{
		SimilarityThreshold:     0.90,
		TTL:                     new(1),
		MaxConversationMessages: new(10),
	}, nil)

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"expiry test"}]}`)
	serveSemanticRequest(t, m, body, "")
	m.wg.Wait()

	require.Equal(t, 1, store.Len())

	time.Sleep(2 * time.Second)
	err := store.DeleteExpired(context.Background())
	require.NoError(t, err)
	require.Equal(t, 0, store.Len())
}

func TestMapVecStore_DeleteExpiredOnlyRemovesExpired(t *testing.T) {
	store := NewMapVecStore()
	ctx := context.Background()

	_ = store.Insert(ctx, "key-expired", []float32{1, 0}, nil, "ph", -1*time.Second)
	_ = store.Insert(ctx, "key-live", []float32{0, 1}, nil, "ph", time.Hour)

	_ = store.DeleteExpired(ctx)

	require.Equal(t, 1, store.Len())

	results, _ := store.Search(ctx, []float32{0, 1}, "ph", 1)
	require.NotEmpty(t, results)
	require.Equal(t, "key-live", results[0].Key)
}

func TestShouldSkipAllCacheHeaders_CacheControl(t *testing.T) {
	for _, cacheControl := range []string{"private, no-store, max-age=0", "private, no-cache, max-age=0"} {
		header := http.Header{"Cache-Control": []string{cacheControl}}
		require.True(t, shouldSkipAllCacheHeaders(header.Get), "Cache-Control: %s", cacheControl)
	}
}

func TestSemanticCacheMiddleware_HitMarksAuditEntryCacheType(t *testing.T) {
	m, _, _ := newTestSemanticMiddleware(0.90, 10, false)
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"semantic-cache-type"}]}`)

	rec1 := serveSemanticRequest(t, m, body, "")
	require.Empty(t, rec1.Header().Get("X-Cache"))

	m.wg.Wait()

	entry := &auditlog.LogEntry{ID: "semantic-audit-entry"}
	c, rec := echotest.Post(t, "/v1/chat/completions", body, echotest.WithValue(string(auditlog.LogEntryKey), entry))
	err := m.Handle(&echoExchange{c: c}, body, func() error {
		return c.JSON(http.StatusOK, map[string]string{"answer": "42"})
	})
	require.NoError(t, err)
	require.Equal(t, "HIT (semantic)", rec.Header().Get("X-Cache"))
	require.Equal(t, auditlog.CacheTypeSemantic, entry.CacheType)
}
