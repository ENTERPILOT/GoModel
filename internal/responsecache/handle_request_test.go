package responsecache

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/cache"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/usage"
)

type recordingUsageLogger struct {
	entries []*usage.UsageEntry
}

func (l *recordingUsageLogger) Write(entry *usage.UsageEntry) {
	if entry != nil {
		l.entries = append(l.entries, entry)
	}
}

func (l *recordingUsageLogger) Config() usage.Config {
	return usage.Config{Enabled: true}
}

func (l *recordingUsageLogger) Close() error {
	return nil
}

type recordingAuditLogger struct {
	config  auditlog.Config
	entries []*auditlog.LogEntry
}

func (l *recordingAuditLogger) Write(entry *auditlog.LogEntry) {
	if entry != nil {
		l.entries = append(l.entries, entry)
	}
}

func (l *recordingAuditLogger) Config() auditlog.Config {
	return l.config
}

func (l *recordingAuditLogger) Close() error {
	return nil
}

func TestHandleRequest_SemanticMissPopulatesExactCache(t *testing.T) {
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

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"handle-request-exact-backfill"}]}`)

	handlerCalls := 0
	run := func() *httptest.ResponseRecorder {
		t.Helper()
		c, rec := echotest.Post(t, "/v1/chat/completions", body)
		err := m.HandleRequest(c, body, func() error {
			handlerCalls++
			return c.JSON(http.StatusOK, map[string]string{"n": "1"})
		})
		require.NoError(t, err)

		return rec
	}

	rec1 := run()
	require.Empty(t, rec1.Header().Get("X-Cache"))
	require.Equal(t, 1, handlerCalls)

	m.simple.wg.Wait()
	m.semantic.wg.Wait()

	rec2 := run()
	require.Equal(t, "HIT (exact)", rec2.Header().Get("X-Cache"))
	require.Equal(t, 1, handlerCalls)
}

func TestHandleInternalRequest_RejectsNilContext(t *testing.T) {
	m := NewResponseCacheMiddlewareWithStore(cache.NewMapStore(), time.Hour)
	var nilCtx context.Context

	_, err := m.HandleInternalRequest(nilCtx, http.MethodPost, "/v1/chat/completions", []byte(`{}`), func(context.Context) (*InternalResponse, error) {
		return &InternalResponse{StatusCode: http.StatusOK, ContentType: "application/json", Body: []byte(`{"ok":"1"}`)}, nil
	})
	require.Error(t, err)

	gatewayErr, ok := err.(*core.GatewayError)
	require.True(t, ok)
	require.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
}

func TestInternalRequestHeaders_AllowlistsSafeSnapshotHeaders(t *testing.T) {
	ctx := core.WithRequestID(context.Background(), "req_123")
	ctx = core.WithRequestSnapshot(ctx, core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/chat/completions",
		nil,
		nil,
		http.Header{
			"Accept":        []string{"application/json"},
			"Authorization": []string{"Bearer secret"},
			"Baggage":       []string{"user_id=123"},
			"Cache-Control": []string{"no-store"},
			"Cookie":        []string{"session=secret"},
			"Traceparent":   []string{"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00"},
			"User-Agent":    []string{"gomodel-test"},
			"X-Api-Key":     []string{"secret-key"},
		},
		"application/json",
		nil,
		false,
		"snapshot_req",
		nil,
		"/team/alpha",
	))

	headers := internalRequestHeaders(ctx)
	require.Equal(t, "application/json", headers.Get("Accept"))
	require.Equal(t, "gomodel-test", headers.Get("User-Agent"))
	require.NotEmpty(t, headers.Get("Traceparent"))
	require.Equal(t, "user_id=123", headers.Get("Baggage"))
	require.Equal(t, "no-store", headers.Get("Cache-Control"))
	require.Equal(t, "application/json", headers.Get("Content-Type"))
	require.Equal(t, "req_123", headers.Get("X-Request-ID"))
	require.Empty(t, headers.Get("Authorization"))
	require.Empty(t, headers.Get("Cookie"))
	require.Empty(t, headers.Get("X-Api-Key"))
}

func TestHandleInternalRequest_RejectsNilMiddleware(t *testing.T) {
	var m *ResponseCacheMiddleware

	_, err := m.HandleInternalRequest(context.Background(), http.MethodPost, "/v1/chat/completions", []byte(`{}`), func(context.Context) (*InternalResponse, error) {
		return &InternalResponse{StatusCode: http.StatusOK, ContentType: "application/json", Body: []byte(`{"ok":"1"}`)}, nil
	})
	require.Error(t, err)

	gatewayErr, ok := err.(*core.GatewayError)
	require.True(t, ok)
	require.Equal(t, core.ErrorTypeProvider, gatewayErr.Type)
	require.Equal(t, http.StatusInternalServerError, gatewayErr.HTTPStatusCode())
}

func TestHandleInternalRequest_NormalizesNonGatewayErrors(t *testing.T) {
	m := NewResponseCacheMiddlewareWithStore(cache.NewMapStore(), time.Hour)
	originalErr := errors.New("cache executor failed")

	_, err := m.HandleInternalRequest(context.Background(), http.MethodPost, "/v1/chat/completions", []byte(`{}`), func(context.Context) (*InternalResponse, error) {
		return nil, originalErr
	})
	require.Error(t, err)

	gatewayErr, ok := err.(*core.GatewayError)
	require.True(t, ok)
	require.Equal(t, core.ErrorTypeProvider, gatewayErr.Type)
	require.Equal(t, originalErr.Error(), gatewayErr.Message)
	require.ErrorIs(t, gatewayErr, originalErr)
}

func TestHandleInternalRequest_ZeroValueMiddlewareIsNoOpCache(t *testing.T) {
	// A zero-value middleware has no cache layers configured; internal
	// requests must pass straight through to the LLM call.
	m := &ResponseCacheMiddleware{}
	calls := 0

	result, err := m.HandleInternalRequest(context.Background(), http.MethodPost, "/v1/chat/completions", []byte(`{}`), func(context.Context) (*InternalResponse, error) {
		calls++
		return &InternalResponse{StatusCode: http.StatusOK, ContentType: "application/json", Body: []byte(`{"ok":"1"}`)}, nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Equal(t, http.StatusOK, result.StatusCode)
	require.Equal(t, `{"ok":"1"}`, string(result.Body))
	require.Empty(t, result.CacheType)
}

func TestHandleInternalRequest_ExactMissThenHit(t *testing.T) {
	m := NewResponseCacheMiddlewareWithStore(cache.NewMapStore(), time.Hour)
	body := []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}`)
	response := `{"id":"chatcmpl-1","choices":[]}`
	calls := 0

	run := func() *InternalHandleResult {
		t.Helper()
		result, err := m.HandleInternalRequest(context.Background(), http.MethodPost, "/v1/chat/completions", body, func(context.Context) (*InternalResponse, error) {
			calls++
			return &InternalResponse{StatusCode: http.StatusOK, ContentType: "application/json", Body: []byte(response)}, nil
		})
		require.NoError(t, err)

		return result
	}

	first := run()
	require.Empty(t, first.CacheType)
	require.Equal(t, 1, calls)

	m.simple.wg.Wait()

	second := run()
	require.Equal(t, CacheTypeExact, second.CacheType)
	require.Equal(t, 1, calls)
	require.Equal(t, response, string(second.Body))
	require.Equal(t, http.StatusOK, second.StatusCode)
	require.Equal(t, CacheHeaderExact, second.Headers.Get("X-Cache"))

	// FailoverUsed responses must never be stored.
	failoverBody := []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"failover"}]}`)
	_, err := m.HandleInternalRequest(context.Background(), http.MethodPost, "/v1/chat/completions", failoverBody, func(context.Context) (*InternalResponse, error) {
		return &InternalResponse{StatusCode: http.StatusOK, ContentType: "application/json", Body: []byte(response), FailoverUsed: true}, nil
	})
	require.NoError(t, err)

	m.simple.wg.Wait()
	followUp, err := m.HandleInternalRequest(context.Background(), http.MethodPost, "/v1/chat/completions", failoverBody, func(context.Context) (*InternalResponse, error) {
		return &InternalResponse{StatusCode: http.StatusOK, ContentType: "application/json", Body: []byte(response)}, nil
	})
	require.NoError(t, err)
	require.Empty(t, followUp.CacheType)
}

func TestInternalCacheType_ParsesHeaderShapes(t *testing.T) {
	cases := []struct {
		headerValue string
		want        string
	}{
		{headerValue: CacheHeaderExact, want: CacheTypeExact},
		{headerValue: CacheHeaderSemantic, want: CacheTypeSemantic},
		{headerValue: "HIT ( semantic )", want: CacheTypeSemantic},
		{headerValue: "  HIT (exact)  ", want: CacheTypeExact},
		{headerValue: CacheTypeExact, want: CacheTypeExact},
		{headerValue: CacheTypeSemantic, want: CacheTypeSemantic},
		{headerValue: "HIT (unknown-cache)", want: ""},
		{headerValue: "MISS", want: ""},
		{headerValue: "", want: ""},
	}

	for _, tc := range cases {
		require.Equal(t, tc.want, internalCacheType(tc.headerValue), "internalCacheType(%q)", tc.headerValue)
	}
}

func TestHandleRequest_FailoverUsedSkipsCacheWrites(t *testing.T) {
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

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"fallback-skip-cache"}]}`)
	handlerCalls := 0

	run := func(markFailover bool) *httptest.ResponseRecorder {
		t.Helper()
		c, rec := echotest.Post(t, "/v1/chat/completions", body)
		err := m.HandleRequest(c, body, func() error {
			handlerCalls++
			if markFailover {
				c.SetRequest(c.Request().WithContext(core.WithFailoverUsed(c.Request().Context())))
			}
			return c.JSON(http.StatusOK, map[string]string{"n": "1"})
		})
		require.NoError(t, err)

		return rec
	}

	rec1 := run(true)
	require.Empty(t, rec1.Header().Get("X-Cache"))
	require.Equal(t, 1, handlerCalls)

	m.simple.wg.Wait()
	m.semantic.wg.Wait()

	rec2 := run(false)
	require.Empty(t, rec2.Header().Get("X-Cache"))
	require.Equal(t, 2, handlerCalls)
}

func TestHandleRequest_ExactHitMarksAuditEntryCacheType(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()

	m := &ResponseCacheMiddleware{
		simple: newSimpleCacheMiddleware(store, time.Hour, nil),
	}

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"mark-exact-cache-type"}]}`)

	run := func() (*httptest.ResponseRecorder, *auditlog.LogEntry) {
		t.Helper()
		entry := &auditlog.LogEntry{ID: "audit-entry"}
		c, rec := echotest.Post(t, "/v1/chat/completions", body, echotest.WithValue(string(auditlog.LogEntryKey), entry))
		err := m.HandleRequest(c, body, func() error {
			return c.JSON(http.StatusOK, map[string]string{"n": "1"})
		})
		require.NoError(t, err)

		return rec, entry
	}

	rec1, entry1 := run()
	require.Empty(t, rec1.Header().Get("X-Cache"))
	require.Empty(t, entry1.CacheType)

	m.simple.wg.Wait()

	rec2, entry2 := run()
	require.Equal(t, "HIT (exact)", rec2.Header().Get("X-Cache"))
	require.Equal(t, auditlog.CacheTypeExact, entry2.CacheType)
}

func TestHandleRequest_ExactHitWritesSyntheticUsageEntry(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()

	logger := &recordingUsageLogger{}
	m := &ResponseCacheMiddleware{
		simple: newSimpleCacheMiddleware(store, time.Hour, newUsageHitRecorder(logger, nil)),
	}

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"cache-usage-hit"}]}`)
	workflow := resolvedWorkflow("openai", "gpt-4")

	run := func() *httptest.ResponseRecorder {
		t.Helper()
		c, rec := postWithWorkflow(t, "/v1/chat/completions", body, workflow)
		err := m.HandleRequest(c, body, func() error {
			return c.JSON(http.StatusOK, &core.ChatResponse{
				ID:    "chatcmpl-cache-hit",
				Model: "gpt-4",
				Usage: core.Usage{
					PromptTokens:     11,
					CompletionTokens: 5,
					TotalTokens:      16,
				},
			})
		})
		require.NoError(t, err)

		return rec
	}

	rec1 := run()
	require.Empty(t, rec1.Header().Get("X-Cache"))

	m.simple.wg.Wait()

	rec2 := run()
	require.Equal(t, "HIT (exact)", rec2.Header().Get("X-Cache"))
	require.Len(t, logger.entries, 1)

	entry := logger.entries[0]
	require.Equal(t, usage.CacheTypeExact, entry.CacheType)
	require.Equal(t, 11, entry.InputTokens)
	require.Equal(t, 5, entry.OutputTokens)
	require.Equal(t, 16, entry.TotalTokens)
}

// TestHandleRequest_EmbeddingsExactHitWritesUsageEntry covers /v1/embeddings on
// the exact layer: the hit replays the stored vector and records the usage row
// a cached chat hit records, with the embeddings token shape.
func TestHandleRequest_EmbeddingsExactHitWritesUsageEntry(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()

	logger := &recordingUsageLogger{}
	m := &ResponseCacheMiddleware{
		simple: newSimpleCacheMiddleware(store, time.Hour, newUsageHitRecorder(logger, nil)),
	}

	body := []byte(`{"model":"text-embedding-3-small","input":"cache-embeddings-hit"}`)
	workflow := resolvedWorkflow("openai", "text-embedding-3-small")

	providerCalls := 0
	run := func() *httptest.ResponseRecorder {
		t.Helper()
		c, rec := postWithWorkflow(t, "/v1/embeddings", body, workflow)
		err := m.HandleRequest(c, body, func() error {
			providerCalls++
			return c.JSON(http.StatusOK, &core.EmbeddingResponse{
				Object: "list",
				Model:  "text-embedding-3-small",
				Data: []core.EmbeddingData{
					{Object: "embedding", Embedding: []byte(`[0.5,0.25]`), Index: 0},
				},
				Usage: core.EmbeddingUsage{PromptTokens: 7, TotalTokens: 7},
			})
		})
		require.NoError(t, err)

		return rec
	}

	rec1 := run()
	require.Empty(t, rec1.Header().Get("X-Cache"))

	m.simple.wg.Wait()

	rec2 := run()
	require.Equal(t, "HIT (exact)", rec2.Header().Get("X-Cache"))
	require.Equal(t, 1, providerCalls)
	require.Equal(t, rec1.Body.String(), rec2.Body.String())
	require.Len(t, logger.entries, 1)

	entry := logger.entries[0]
	require.Equal(t, usage.CacheTypeExact, entry.CacheType)
	require.Equal(t, "/v1/embeddings", entry.Endpoint)
	require.Equal(t, 7, entry.InputTokens)
	require.Equal(t, 0, entry.OutputTokens)
	require.Equal(t, 7, entry.TotalTokens)
}

// TestHandleRequest_EmbeddingsSkipSemanticLayer pins that embeddings never take
// part in semantic caching: a near-match must not answer with another input's
// vector, and the lookup must not spend an embedder call.
func TestHandleRequest_EmbeddingsSkipSemanticLayer(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()

	emb := &mockEmbedder{vector: []float32{1, 0, 0}}
	vecStore := NewMapVecStore()
	semCfg := config.SemanticCacheConfig{
		SimilarityThreshold:     0.50,
		TTL:                     new(3600),
		MaxConversationMessages: new(10),
	}
	m := &ResponseCacheMiddleware{
		simple:   newSimpleCacheMiddleware(store, time.Hour, nil),
		semantic: newSemanticCacheMiddleware(emb, vecStore, semCfg, nil),
	}

	providerCalls := 0
	run := func(input string) *httptest.ResponseRecorder {
		t.Helper()
		body := []byte(`{"model":"text-embedding-3-small","input":"` + input + `"}`)
		c, rec := echotest.Post(t, "/v1/embeddings", body)
		err := m.HandleRequest(c, body, func() error {
			providerCalls++
			return c.JSON(http.StatusOK, map[string]string{"input": input})
		})
		require.NoError(t, err)

		return rec
	}

	run("the cat sat on the mat")
	m.simple.wg.Wait()
	m.semantic.wg.Wait()

	rec := run("a cat sat upon the mat")
	require.Empty(t, rec.Header().Get("X-Cache"))
	require.Equal(t, 2, providerCalls)
	require.Equal(t, 0, emb.calls)
	require.Equal(t, 0, vecStore.Len())
}

func TestHandleRequest_AuditMiddlewarePreservesCommittedErrorStatus(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()

	m := &ResponseCacheMiddleware{
		simple: newSimpleCacheMiddleware(store, time.Hour, nil),
	}
	logger := &recordingAuditLogger{
		config: auditlog.Config{
			Enabled:   true,
			LogBodies: true,
		},
	}

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"cache-audit-error-status"}]}`)
	c, rec := echotest.Post(t, "/v1/chat/completions", body)

	handler := auditlog.Middleware(logger)(func(c *echo.Context) error {
		return m.HandleRequest(c, body, func() error {
			return c.JSON(http.StatusGatewayTimeout, map[string]any{
				"error": map[string]any{
					"message": "provider timeout",
				},
			})
		})
	})
	err := handler(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusGatewayTimeout, rec.Code)
	require.Len(t, logger.entries, 1)
	require.Equal(t, http.StatusGatewayTimeout, logger.entries[0].StatusCode)
}

func TestHandleRequest_GatewayTimeoutDoesNotPopulateExactCache(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()

	m := &ResponseCacheMiddleware{
		simple: newSimpleCacheMiddleware(store, time.Hour, nil),
	}

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"do-not-cache-timeout"}]}`)
	handlerCalls := 0

	run := func() *httptest.ResponseRecorder {
		t.Helper()
		c, rec := echotest.Post(t, "/v1/chat/completions", body)
		err := m.HandleRequest(c, body, func() error {
			handlerCalls++
			return c.JSON(http.StatusGatewayTimeout, map[string]any{
				"error": map[string]any{
					"message": "timeout awaiting response headers",
				},
			})
		})
		require.NoError(t, err)

		return rec
	}

	rec1 := run()
	require.Equal(t, http.StatusGatewayTimeout, rec1.Code)
	require.Empty(t, rec1.Header().Get("X-Cache"))

	m.simple.wg.Wait()

	rec2 := run()
	require.Equal(t, http.StatusGatewayTimeout, rec2.Code)
	require.Empty(t, rec2.Header().Get("X-Cache"))
	require.Equal(t, 2, handlerCalls)
}

func TestHandleRequest_GatewayTimeoutDoesNotPopulateSemanticCache(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()

	emb := &mockEmbedder{vector: []float32{1, 0, 0}}
	vecStore := NewMapVecStore()
	semCfg := config.SemanticCacheConfig{
		Enabled:                 new(true),
		SimilarityThreshold:     0.90,
		TTL:                     new(3600),
		MaxConversationMessages: new(10),
	}
	m := &ResponseCacheMiddleware{
		simple:   newSimpleCacheMiddleware(store, time.Hour, nil),
		semantic: newSemanticCacheMiddleware(emb, vecStore, semCfg, nil),
	}

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"do-not-semantic-cache-timeout"}]}`)
	handlerCalls := 0

	run := func() *httptest.ResponseRecorder {
		t.Helper()
		c, rec := echotest.Post(t, "/v1/chat/completions", body, echotest.WithHeader("X-Cache-Type", CacheTypeSemantic))
		err := m.HandleRequest(c, body, func() error {
			handlerCalls++
			return c.JSON(http.StatusGatewayTimeout, map[string]any{
				"error": map[string]any{
					"message": "timeout awaiting response headers",
				},
			})
		})
		require.NoError(t, err)

		return rec
	}

	rec1 := run()
	require.Equal(t, http.StatusGatewayTimeout, rec1.Code)
	require.Empty(t, rec1.Header().Get("X-Cache"))

	m.simple.wg.Wait()
	m.semantic.wg.Wait()

	rec2 := run()
	require.Equal(t, http.StatusGatewayTimeout, rec2.Code)
	require.Empty(t, rec2.Header().Get("X-Cache"))
	require.Equal(t, 2, handlerCalls)
}

func TestHandleRequest_CacheControlNoCacheBypassesAllLayers(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()

	emb := &mockEmbedder{vector: []float32{1, 0, 0}}
	vecStore := NewMapVecStore()
	semCfg := config.SemanticCacheConfig{
		Enabled:                 new(true),
		SimilarityThreshold:     0.90,
		TTL:                     new(3600),
		MaxConversationMessages: new(10),
	}

	m := &ResponseCacheMiddleware{
		simple:   newSimpleCacheMiddleware(store, time.Hour, nil),
		semantic: newSemanticCacheMiddleware(emb, vecStore, semCfg, nil),
	}

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"handle-request-no-cache"}]}`)
	handlerCalls := 0

	run := func(cacheControl string) *httptest.ResponseRecorder {
		t.Helper()
		var opts []echotest.Option
		if cacheControl != "" {
			opts = append(opts, echotest.WithHeader("Cache-Control", cacheControl))
		}
		c, rec := echotest.Post(t, "/v1/chat/completions", body, opts...)
		err := m.HandleRequest(c, body, func() error {
			handlerCalls++
			return c.JSON(http.StatusOK, map[string]int{"n": handlerCalls})
		})
		require.NoError(t, err)

		return rec
	}

	rec1 := run("")
	require.Empty(t, rec1.Header().Get("X-Cache"))

	m.simple.wg.Wait()
	m.semantic.wg.Wait()

	rec2 := run("no-cache")
	require.Empty(t, rec2.Header().Get("X-Cache"))
	require.Contains(t, rec2.Body.String(), `"n":2`, "no-cache must reach the handler")

	rec3 := run("")
	require.Equal(t, "HIT (exact)", rec3.Header().Get("X-Cache"))
	require.Contains(t, rec3.Body.String(), `"n":1`, "hit must replay the original cached payload")
	require.Equal(t, 2, handlerCalls)
}

func TestHandleRequest_StreamingMissPopulatesExactStreamingCacheOnly(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()

	m := &ResponseCacheMiddleware{
		simple: newSimpleCacheMiddleware(store, time.Hour, nil),
	}

	streamBody := []byte(`{"model":"gpt-4","stream":true,"messages":[{"role":"user","content":"cache-streaming-cross-mode"}]}`)
	jsonBody := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"cache-streaming-cross-mode"}]}`)
	rawStream := []byte(
		"data: {\"id\":\"chatcmpl-stream-cache\",\"object\":\"chat.completion.chunk\",\"created\":1234567890,\"model\":\"gpt-4\",\"provider\":\"openai\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hello\"},\"finish_reason\":null}]}\n\n" +
			"data: {\"id\":\"chatcmpl-stream-cache\",\"object\":\"chat.completion.chunk\",\"created\":1234567890,\"model\":\"gpt-4\",\"provider\":\"openai\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":2,\"total_tokens\":13}}\n\n" +
			"data: [DONE]\n\n",
	)
	workflow := resolvedWorkflow("openai", "gpt-4")
	handlerCalls := 0

	run := func(body []byte) *httptest.ResponseRecorder {
		t.Helper()
		c, rec := postWithWorkflow(t, "/v1/chat/completions", body, workflow)
		err := m.HandleRequest(c, body, func() error {
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
	require.Equal(t, "text/event-stream", rec1.Header().Get("Content-Type"))
	require.Equal(t, 1, handlerCalls)
	require.Equal(t, rawStream, rec1.Body.Bytes())

	m.simple.wg.Wait()

	rec2 := run(jsonBody)
	require.Empty(t, rec2.Header().Get("X-Cache"))
	require.Equal(t, "application/json", rec2.Header().Get("Content-Type"))
	require.Contains(t, rec2.Body.String(), `"mode":"json"`)
	require.Equal(t, 2, handlerCalls)

	m.simple.wg.Wait()

	rec3 := run(streamBody)
	require.Equal(t, "HIT (exact)", rec3.Header().Get("X-Cache"))
	require.Equal(t, "text/event-stream", rec3.Header().Get("Content-Type"))
	require.Equal(t, rawStream, rec3.Body.Bytes())
	require.Equal(t, 2, handlerCalls)

	rec4 := run(jsonBody)
	require.Equal(t, "HIT (exact)", rec4.Header().Get("X-Cache"))
	require.Equal(t, "application/json", rec4.Header().Get("Content-Type"))
	require.Contains(t, rec4.Body.String(), `"mode":"json"`)
	require.Equal(t, 2, handlerCalls)
}

func TestHandleRequest_StreamingExactHitWritesSyntheticUsageEntry(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()

	logger := &recordingUsageLogger{}
	m := &ResponseCacheMiddleware{
		simple: newSimpleCacheMiddleware(store, time.Hour, newUsageHitRecorder(logger, nil)),
	}

	body := []byte(`{"model":"gpt-4","stream":true,"messages":[{"role":"user","content":"cache-stream-usage-hit"}]}`)
	rawStream := []byte(
		"data: {\"id\":\"chatcmpl-cache-hit\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":null}]}\n\n" +
			"data: {\"id\":\"chatcmpl-cache-hit\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":5,\"total_tokens\":16}}\n\n" +
			"data: [DONE]\n\n",
	)
	workflow := resolvedWorkflow("openai", "gpt-4")

	run := func() *httptest.ResponseRecorder {
		t.Helper()
		c, rec := postWithWorkflow(t, "/v1/chat/completions", body, workflow)
		err := m.HandleRequest(c, body, func() error {
			c.Response().Header().Set("Content-Type", "text/event-stream")
			c.Response().WriteHeader(http.StatusOK)
			_, _ = c.Response().Write(rawStream)
			return nil
		})
		require.NoError(t, err)

		return rec
	}

	rec1 := run()
	require.Empty(t, rec1.Header().Get("X-Cache"))

	m.simple.wg.Wait()

	rec2 := run()
	require.Equal(t, "HIT (exact)", rec2.Header().Get("X-Cache"))
	require.Len(t, logger.entries, 1)

	entry := logger.entries[0]
	require.Equal(t, usage.CacheTypeExact, entry.CacheType)
	require.Equal(t, 11, entry.InputTokens)
	require.Equal(t, 5, entry.OutputTokens)
	require.Equal(t, 16, entry.TotalTokens)
	require.Equal(t, "chatcmpl-cache-hit", entry.ProviderID)
}

func TestHandleRequest_StreamingExactHitAuditLogsCachedResponseBody(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()

	m := &ResponseCacheMiddleware{
		simple: newSimpleCacheMiddleware(store, time.Hour, nil),
	}
	logger := &recordingAuditLogger{
		config: auditlog.Config{
			Enabled:    true,
			LogBodies:  true,
			LogHeaders: true,
		},
	}

	body := []byte(`{"model":"gpt-4","stream":true,"messages":[{"role":"user","content":"cache-stream-audit-hit"}]}`)
	rawStream := []byte(
		"data: {\"id\":\"chatcmpl-cache-audit\",\"object\":\"chat.completion.chunk\",\"created\":1234567890,\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hello\"},\"finish_reason\":null}]}\n\n" +
			"data: {\"id\":\"chatcmpl-cache-audit\",\"object\":\"chat.completion.chunk\",\"created\":1234567890,\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" cached audit\"},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: [DONE]\n\n",
	)
	workflow := resolvedWorkflow("openai", "gpt-4")

	run := func() *httptest.ResponseRecorder {
		t.Helper()
		c, rec := postWithWorkflow(t, "/v1/chat/completions", body, workflow, echotest.WithHeader("X-Request-ID", "req-cache-audit"))
		handler := auditlog.Middleware(logger)(func(c *echo.Context) error {
			return m.HandleRequest(c, body, func() error {
				auditlog.MarkEntryAsStreaming(c, true)
				auditlog.EnrichEntryWithStream(c, true)
				c.Response().Header().Set("Content-Type", "text/event-stream")
				c.Response().WriteHeader(http.StatusOK)
				_, _ = c.Response().Write(rawStream)
				return nil
			})
		})
		err := handler(c)
		require.NoError(t, err)

		return rec
	}

	rec1 := run()
	require.Empty(t, rec1.Header().Get("X-Cache"))

	m.simple.wg.Wait()
	require.Empty(t, logger.entries)

	rec2 := run()
	require.Equal(t, "HIT (exact)", rec2.Header().Get("X-Cache"))
	require.Len(t, logger.entries, 1)

	entry := logger.entries[0]
	require.True(t, entry.Stream)
	require.Equal(t, auditlog.CacheTypeExact, entry.CacheType)
	require.NotNil(t, entry.Data)
	require.NotNil(t, entry.Data.ResponseBody)

	response, ok := entry.Data.ResponseBody.(map[string]any)
	require.True(t, ok, "response body type = %T, want map[string]any", entry.Data.ResponseBody)

	choices, ok := response["choices"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, choices, 1)

	message, ok := choices[0]["message"].(map[string]any)
	require.True(t, ok, "message = %#v, want map[string]any", choices[0]["message"])
	require.Equal(t, "Hello cached audit", message["content"])
	require.Equal(t, "HIT (exact)", entry.Data.ResponseHeaders["X-Cache"])
}

func TestHandleRequest_InvalidStreamingBodySkipsExactCacheWrite(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()

	m := &ResponseCacheMiddleware{
		simple: newSimpleCacheMiddleware(store, time.Hour, nil),
	}

	body := []byte(`{"model":"gpt-4","stream":true,"messages":[{"role":"user","content":"invalid-stream-cache"}]}`)
	invalidStream := []byte(
		"data: {\"id\":\"chatcmpl-invalid\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n",
	)
	workflow := resolvedWorkflow("openai", "gpt-4")
	handlerCalls := 0

	run := func() *httptest.ResponseRecorder {
		t.Helper()
		c, rec := postWithWorkflow(t, "/v1/chat/completions", body, workflow)
		err := m.HandleRequest(c, body, func() error {
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

	m.simple.wg.Wait()

	rec2 := run()
	require.Empty(t, rec2.Header().Get("X-Cache"))
	require.Equal(t, 2, handlerCalls)
}

// failingResponseWriter simulates a client that stopped draining the
// connection: Write returns writeErr, and StallError reports flushErr the way
// the server's stall deadline writer reports a stalled flush.
type failingResponseWriter struct {
	http.ResponseWriter
	writeErr error
	flushErr error
}

func (w *failingResponseWriter) Write(p []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return w.ResponseWriter.Write(p)
}

func (w *failingResponseWriter) StallError() error           { return w.flushErr }
func (w *failingResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// TestHandleRequest_StreamHitReportsClientWriteFailure returns the write or
// flush error of a cached stream replay as a ReplayError, so the caller can
// record a stalled or vanished client instead of a clean 200.
func TestHandleRequest_StreamHitReportsClientWriteFailure(t *testing.T) {
	stallErr := errors.New("client stopped reading the response for 3s: write tcp: i/o timeout")
	tests := []struct {
		name     string
		writeErr error
		flushErr error
	}{
		{name: "write fails", writeErr: syscall.EPIPE},
		{name: "final flush stalls", flushErr: stallErr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := cache.NewMapStore()
			defer store.Close()
			m := &ResponseCacheMiddleware{simple: newSimpleCacheMiddleware(store, time.Hour, nil)}
			body := []byte(`{"model":"gpt-4","stream":true,"messages":[{"role":"user","content":"replay-` + tt.name + `"}]}`)
			rawStream := []byte("data: {\"id\":\"chatcmpl-stream\",\"object\":\"chat.completion.chunk\",\"created\":1234567890,\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"streamed\"},\"finish_reason\":null}]}\n\n" +
				"data: {\"id\":\"chatcmpl-stream\",\"object\":\"chat.completion.chunk\",\"created\":1234567890,\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":1,\"total_tokens\":10}}\n\n" +
				"data: [DONE]\n\n")

			primeCtx, _ := echotest.Post(t, "/v1/chat/completions", body)
			err := m.HandleRequest(primeCtx, body, func() error {
				primeCtx.Response().Header().Set("Content-Type", "text/event-stream")
				primeCtx.Response().WriteHeader(http.StatusOK)
				_, _ = primeCtx.Response().Write(rawStream)
				return nil
			})
			require.NoError(t, err)

			m.simple.wg.Wait()

			// The replay needs a writer that fails, which echotest cannot supply.
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			c := echo.New().NewContext(req, &failingResponseWriter{ResponseWriter: httptest.NewRecorder(), writeErr: tt.writeErr, flushErr: tt.flushErr})
			err = m.HandleRequest(c, body, func() error {
				t.Fatal("cached stream must not reach the handler")
				return nil
			})
			_, ok := errors.AsType[*ReplayError](err)
			require.True(t, ok, "err = %v, want *ReplayError", err)

			want := tt.writeErr
			if want == nil {
				want = tt.flushErr
			}
			require.ErrorIs(t, err, want)
			require.Equal(t, "HIT (exact)", c.Response().Header().Get("X-Cache"))
		})
	}
}
