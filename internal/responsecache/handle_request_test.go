package responsecache

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

	"gomodel/config"
	"gomodel/internal/auditlog"
	"gomodel/internal/cache"
	"gomodel/internal/core"
	"gomodel/internal/usage"
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

func TestHandleRequest_SemanticMissPopulatesExactCache(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()

	emb := &mockEmbedder{vector: []float32{1, 0, 0}}
	vecStore := NewMapVecStore()
	semCfg := config.SemanticCacheConfig{
		SimilarityThreshold:     0.90,
		TTL:                     intPtr(3600),
		MaxConversationMessages: intPtr(10),
	}

	m := &ResponseCacheMiddleware{
		simple:   newSimpleCacheMiddleware(store, time.Hour, nil),
		semantic: newSemanticCacheMiddleware(emb, vecStore, semCfg, nil),
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
		TTL:                     intPtr(3600),
		MaxConversationMessages: intPtr(10),
	}

	m := &ResponseCacheMiddleware{
		simple:   newSimpleCacheMiddleware(store, time.Hour, nil),
		semantic: newSemanticCacheMiddleware(emb, vecStore, semCfg, nil),
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

func TestHandleRequest_ExactHitMarksAuditEntryCacheType(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()

	m := &ResponseCacheMiddleware{
		simple: newSimpleCacheMiddleware(store, time.Hour, nil),
	}

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"mark-exact-cache-type"}]}`)
	e := echo.New()

	run := func() (*httptest.ResponseRecorder, *auditlog.LogEntry) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		entry := &auditlog.LogEntry{ID: "audit-entry"}
		c.Set(string(auditlog.LogEntryKey), entry)
		if err := m.HandleRequest(c, body, func() error {
			return c.JSON(http.StatusOK, map[string]string{"n": "1"})
		}); err != nil {
			t.Fatalf("HandleRequest: %v", err)
		}
		return rec, entry
	}

	rec1, entry1 := run()
	if rec1.Header().Get("X-Cache") != "" {
		t.Fatalf("first request should miss exact cache, got X-Cache=%q", rec1.Header().Get("X-Cache"))
	}
	if entry1.CacheType != "" {
		t.Fatalf("first request CacheType = %q, want empty", entry1.CacheType)
	}

	m.simple.wg.Wait()

	rec2, entry2 := run()
	if rec2.Header().Get("X-Cache") != "HIT (exact)" {
		t.Fatalf("second request should be exact hit, got X-Cache=%q", rec2.Header().Get("X-Cache"))
	}
	if entry2.CacheType != auditlog.CacheTypeExact {
		t.Fatalf("second request CacheType = %q, want %q", entry2.CacheType, auditlog.CacheTypeExact)
	}
}

func TestHandleRequest_ExactHitWritesSyntheticUsageEntry(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()

	logger := &recordingUsageLogger{}
	m := &ResponseCacheMiddleware{
		simple: newSimpleCacheMiddleware(store, time.Hour, newUsageHitRecorder(logger, nil)),
	}

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"cache-usage-hit"}]}`)
	e := echo.New()

	run := func() *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		plan := &core.ExecutionPlan{
			Mode:         core.ExecutionModeTranslated,
			ProviderType: "openai",
			Resolution: &core.RequestModelResolution{
				ResolvedSelector: core.ModelSelector{Provider: "openai", Model: "gpt-4"},
			},
		}
		c.SetRequest(req.WithContext(core.WithExecutionPlan(req.Context(), plan)))
		if err := m.HandleRequest(c, body, func() error {
			return c.JSON(http.StatusOK, &core.ChatResponse{
				ID:    "chatcmpl-cache-hit",
				Model: "gpt-4",
				Usage: core.Usage{
					PromptTokens:     11,
					CompletionTokens: 5,
					TotalTokens:      16,
				},
			})
		}); err != nil {
			t.Fatalf("HandleRequest: %v", err)
		}
		return rec
	}

	rec1 := run()
	if rec1.Header().Get("X-Cache") != "" {
		t.Fatalf("first request should miss exact cache, got X-Cache=%q", rec1.Header().Get("X-Cache"))
	}

	m.simple.wg.Wait()

	rec2 := run()
	if rec2.Header().Get("X-Cache") != "HIT (exact)" {
		t.Fatalf("second request should be exact hit, got X-Cache=%q", rec2.Header().Get("X-Cache"))
	}
	if len(logger.entries) != 1 {
		t.Fatalf("expected 1 synthetic usage entry, got %d", len(logger.entries))
	}
	entry := logger.entries[0]
	if entry.CacheType != usage.CacheTypeExact {
		t.Fatalf("CacheType = %q, want %q", entry.CacheType, usage.CacheTypeExact)
	}
	if entry.InputTokens != 11 || entry.OutputTokens != 5 || entry.TotalTokens != 16 {
		t.Fatalf("unexpected tokens: %+v", entry)
	}
}

func TestHandleRequest_CacheControlNoCacheBypassesAllLayers(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()

	emb := &mockEmbedder{vector: []float32{1, 0, 0}}
	vecStore := NewMapVecStore()
	semCfg := config.SemanticCacheConfig{
		Enabled:                 boolPtr(true),
		SimilarityThreshold:     0.90,
		TTL:                     intPtr(3600),
		MaxConversationMessages: intPtr(10),
	}

	m := &ResponseCacheMiddleware{
		simple:   newSimpleCacheMiddleware(store, time.Hour, nil),
		semantic: newSemanticCacheMiddleware(emb, vecStore, semCfg, nil),
	}

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"handle-request-no-cache"}]}`)
	e := echo.New()
	handlerCalls := 0

	run := func(cacheControl string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if cacheControl != "" {
			req.Header.Set("Cache-Control", cacheControl)
		}
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		if err := m.HandleRequest(c, body, func() error {
			handlerCalls++
			return c.JSON(http.StatusOK, map[string]int{"n": handlerCalls})
		}); err != nil {
			t.Fatalf("HandleRequest: %v", err)
		}
		return rec
	}

	rec1 := run("")
	if got := rec1.Header().Get("X-Cache"); got != "" {
		t.Fatalf("first request should miss cache, got X-Cache=%q", got)
	}

	m.simple.wg.Wait()
	m.semantic.wg.Wait()

	rec2 := run("no-cache")
	if got := rec2.Header().Get("X-Cache"); got != "" {
		t.Fatalf("no-cache request should bypass cache, got X-Cache=%q", got)
	}
	if !bytes.Contains(rec2.Body.Bytes(), []byte(`"n":2`)) {
		t.Fatalf("no-cache response body = %q, want fresh handler response", rec2.Body.String())
	}

	rec3 := run("")
	if got := rec3.Header().Get("X-Cache"); got != "HIT (exact)" {
		t.Fatalf("follow-up request should still hit original cache entry, got X-Cache=%q", got)
	}
	if !bytes.Contains(rec3.Body.Bytes(), []byte(`"n":1`)) {
		t.Fatalf("cached response body = %q, want original cached payload", rec3.Body.String())
	}
	if handlerCalls != 2 {
		t.Fatalf("expected handler to run exactly twice, got %d calls", handlerCalls)
	}
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
	e := echo.New()
	handlerCalls := 0

	run := func(body []byte) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		plan := &core.ExecutionPlan{
			Mode:         core.ExecutionModeTranslated,
			ProviderType: "openai",
			Resolution: &core.RequestModelResolution{
				ResolvedSelector: core.ModelSelector{Provider: "openai", Model: "gpt-4"},
			},
		}
		c.SetRequest(req.WithContext(core.WithExecutionPlan(req.Context(), plan)))
		if err := m.HandleRequest(c, body, func() error {
			handlerCalls++
			if isStreamingRequest(c.Request().URL.Path, body) {
				c.Response().Header().Set("Content-Type", "text/event-stream")
				c.Response().WriteHeader(http.StatusOK)
				_, _ = c.Response().Write(rawStream)
				return nil
			}
			return c.JSON(http.StatusOK, map[string]string{"mode": "json"})
		}); err != nil {
			t.Fatalf("HandleRequest: %v", err)
		}
		return rec
	}

	rec1 := run(streamBody)
	if got := rec1.Header().Get("X-Cache"); got != "" {
		t.Fatalf("streaming miss should not be cache hit, got X-Cache=%q", got)
	}
	if got := rec1.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("streaming miss Content-Type = %q, want text/event-stream", got)
	}
	if handlerCalls != 1 {
		t.Fatalf("expected 1 handler invocation after streaming miss, got %d", handlerCalls)
	}
	if !bytes.Equal(rec1.Body.Bytes(), rawStream) {
		t.Fatalf("streaming miss body = %q, want original SSE payload", rec1.Body.String())
	}

	m.simple.wg.Wait()

	rec2 := run(jsonBody)
	if got := rec2.Header().Get("X-Cache"); got != "" {
		t.Fatalf("non-streaming follow-up should miss exact cache because stream mode is keyed separately, got X-Cache=%q", got)
	}
	if got := rec2.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("non-streaming miss Content-Type = %q, want application/json", got)
	}
	if !bytes.Contains(rec2.Body.Bytes(), []byte(`"mode":"json"`)) {
		t.Fatalf("non-streaming miss body = %q, want JSON response", rec2.Body.String())
	}
	if handlerCalls != 2 {
		t.Fatalf("non-streaming miss should call handler again, got %d calls", handlerCalls)
	}

	m.simple.wg.Wait()

	rec3 := run(streamBody)
	if got := rec3.Header().Get("X-Cache"); got != "HIT (exact)" {
		t.Fatalf("streaming follow-up should hit its own exact cache entry, got X-Cache=%q", got)
	}
	if got := rec3.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("streaming hit Content-Type = %q, want text/event-stream", got)
	}
	if !bytes.Equal(rec3.Body.Bytes(), rawStream) {
		t.Fatalf("streaming cache hit body = %q, want verbatim SSE replay", rec3.Body.String())
	}
	if handlerCalls != 2 {
		t.Fatalf("streaming exact hit should not call handler again, got %d calls", handlerCalls)
	}

	rec4 := run(jsonBody)
	if got := rec4.Header().Get("X-Cache"); got != "HIT (exact)" {
		t.Fatalf("non-streaming follow-up should hit its own exact cache entry, got X-Cache=%q", got)
	}
	if got := rec4.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("non-streaming hit Content-Type = %q, want application/json", got)
	}
	if !bytes.Contains(rec4.Body.Bytes(), []byte(`"mode":"json"`)) {
		t.Fatalf("non-streaming cache hit body = %q, want cached JSON response", rec4.Body.String())
	}
	if handlerCalls != 2 {
		t.Fatalf("non-streaming exact hit should not call handler again, got %d calls", handlerCalls)
	}
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
	e := echo.New()

	run := func() *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		plan := &core.ExecutionPlan{
			Mode:         core.ExecutionModeTranslated,
			ProviderType: "openai",
			Resolution: &core.RequestModelResolution{
				ResolvedSelector: core.ModelSelector{Provider: "openai", Model: "gpt-4"},
			},
		}
		c.SetRequest(req.WithContext(core.WithExecutionPlan(req.Context(), plan)))
		if err := m.HandleRequest(c, body, func() error {
			c.Response().Header().Set("Content-Type", "text/event-stream")
			c.Response().WriteHeader(http.StatusOK)
			_, _ = c.Response().Write(rawStream)
			return nil
		}); err != nil {
			t.Fatalf("HandleRequest: %v", err)
		}
		return rec
	}

	rec1 := run()
	if got := rec1.Header().Get("X-Cache"); got != "" {
		t.Fatalf("first request should miss exact cache, got X-Cache=%q", got)
	}

	m.simple.wg.Wait()

	rec2 := run()
	if got := rec2.Header().Get("X-Cache"); got != "HIT (exact)" {
		t.Fatalf("second request should be exact hit, got X-Cache=%q", got)
	}
	if len(logger.entries) != 1 {
		t.Fatalf("expected 1 synthetic usage entry, got %d", len(logger.entries))
	}
	entry := logger.entries[0]
	if entry.CacheType != usage.CacheTypeExact {
		t.Fatalf("CacheType = %q, want %q", entry.CacheType, usage.CacheTypeExact)
	}
	if entry.InputTokens != 11 || entry.OutputTokens != 5 || entry.TotalTokens != 16 {
		t.Fatalf("unexpected tokens: %+v", entry)
	}
	if entry.ProviderID != "chatcmpl-cache-hit" {
		t.Fatalf("ProviderID = %q, want chatcmpl-cache-hit", entry.ProviderID)
	}
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
	e := echo.New()
	handlerCalls := 0

	run := func() *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		plan := &core.ExecutionPlan{
			Mode:         core.ExecutionModeTranslated,
			ProviderType: "openai",
			Resolution: &core.RequestModelResolution{
				ResolvedSelector: core.ModelSelector{Provider: "openai", Model: "gpt-4"},
			},
		}
		c.SetRequest(req.WithContext(core.WithExecutionPlan(req.Context(), plan)))
		if err := m.HandleRequest(c, body, func() error {
			handlerCalls++
			c.Response().Header().Set("Content-Type", "text/event-stream")
			c.Response().WriteHeader(http.StatusOK)
			_, _ = c.Response().Write(invalidStream)
			return nil
		}); err != nil {
			t.Fatalf("HandleRequest: %v", err)
		}
		return rec
	}

	rec1 := run()
	if got := rec1.Header().Get("X-Cache"); got != "" {
		t.Fatalf("first request should miss cache, got X-Cache=%q", got)
	}

	m.simple.wg.Wait()

	rec2 := run()
	if got := rec2.Header().Get("X-Cache"); got != "" {
		t.Fatalf("invalid streaming body should not be cached, got X-Cache=%q", got)
	}
	if handlerCalls != 2 {
		t.Fatalf("expected invalid stream to bypass cache on follow-up, got %d calls", handlerCalls)
	}
}

func TestReconstructStreamingResponse_PreservesChatReasoningContent(t *testing.T) {
	raw := []byte(
		"data: {\"id\":\"chatcmpl-reasoning\",\"object\":\"chat.completion.chunk\",\"created\":1234567890,\"model\":\"claude-sonnet\",\"provider\":\"anthropic\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning_content\":\"think first\"},\"finish_reason\":null}]}\n\n" +
			"data: {\"id\":\"chatcmpl-reasoning\",\"object\":\"chat.completion.chunk\",\"created\":1234567890,\"model\":\"claude-sonnet\",\"provider\":\"anthropic\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"final answer\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":4,\"total_tokens\":14}}\n\n" +
			"data: [DONE]\n\n",
	)

	cached, ok := reconstructStreamingResponse("/v1/chat/completions", raw, streamResponseDefaults{
		Model:    "claude-sonnet",
		Provider: "anthropic",
	})
	if !ok {
		t.Fatal("expected streamed chat response to reconstruct successfully")
	}
	if !bytes.Contains(cached, []byte(`"reasoning_content":"think first"`)) {
		t.Fatalf("reconstructed chat response = %q, want reasoning_content preserved", string(cached))
	}

	replay, err := renderCachedChatStream([]byte(`{"model":"claude-sonnet","stream":true}`), cached)
	if err != nil {
		t.Fatalf("renderCachedChatStream() error = %v", err)
	}
	if !bytes.Contains(replay, []byte(`"reasoning_content":"think first"`)) {
		t.Fatalf("cached chat replay = %q, want reasoning_content delta", string(replay))
	}
	if bytes.Contains(replay, []byte(`"usage"`)) {
		t.Fatalf("cached chat replay without include_usage = %q, did not expect usage chunk", string(replay))
	}

	replayWithUsage, err := renderCachedChatStream([]byte(`{"model":"claude-sonnet","stream":true,"stream_options":{"include_usage":true}}`), cached)
	if err != nil {
		t.Fatalf("renderCachedChatStream(include_usage) error = %v", err)
	}
	if !bytes.Contains(replayWithUsage, []byte(`"usage"`)) {
		t.Fatalf("cached chat replay with include_usage = %q, want usage chunk", string(replayWithUsage))
	}
}

func TestRenderCachedChatStream_EmitsStandaloneUsageChunk(t *testing.T) {
	raw := []byte(
		"data: {\"id\":\"chatcmpl-usage\",\"object\":\"chat.completion.chunk\",\"created\":1234567890,\"model\":\"gpt-4o-mini\",\"provider\":\"openai\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hello\"},\"finish_reason\":null}]}\n\n" +
			"data: {\"id\":\"chatcmpl-usage\",\"object\":\"chat.completion.chunk\",\"created\":1234567890,\"model\":\"gpt-4o-mini\",\"provider\":\"openai\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":2,\"total_tokens\":13}}\n\n" +
			"data: [DONE]\n\n",
	)

	cached, ok := reconstructStreamingResponse("/v1/chat/completions", raw, streamResponseDefaults{
		Model:    "gpt-4o-mini",
		Provider: "openai",
	})
	if !ok {
		t.Fatal("expected streamed chat response to reconstruct successfully")
	}

	replay, err := renderCachedChatStream([]byte(`{"model":"gpt-4o-mini","stream":true,"stream_options":{"include_usage":true}}`), cached)
	if err != nil {
		t.Fatalf("renderCachedChatStream() error = %v", err)
	}

	var events []map[string]any
	parseSSEJSONEvents(replay, func(event map[string]any) {
		events = append(events, event)
	})
	if len(events) != 2 {
		t.Fatalf("len(events) = %d, want 2 chat events before [DONE]", len(events))
	}

	firstChoices, ok := events[0]["choices"].([]any)
	if !ok || len(firstChoices) != 1 {
		t.Fatalf("first event choices = %#v, want len=1", events[0]["choices"])
	}
	if _, ok := events[0]["usage"]; ok {
		t.Fatalf("first event should not carry usage, got %#v", events[0]["usage"])
	}

	secondChoices, ok := events[1]["choices"].([]any)
	if !ok || len(secondChoices) != 0 {
		t.Fatalf("usage event choices = %#v, want empty slice", events[1]["choices"])
	}
	usage, ok := events[1]["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage event usage = %#v, want object", events[1]["usage"])
	}
	if got, ok := jsonNumberToInt(usage["total_tokens"]); !ok || got != 13 {
		t.Fatalf("usage.total_tokens = %#v, want 13", usage["total_tokens"])
	}
}

func TestReconstructStreamingResponse_PreservesChatLogprobs(t *testing.T) {
	raw := []byte(
		"data: {\"id\":\"chatcmpl-logprobs\",\"object\":\"chat.completion.chunk\",\"created\":1234567890,\"model\":\"gpt-4o-mini\",\"provider\":\"openai\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hello\"},\"logprobs\":null,\"finish_reason\":null}]}\n\n" +
			"data: {\"id\":\"chatcmpl-logprobs\",\"object\":\"chat.completion.chunk\",\"created\":1234567890,\"model\":\"gpt-4o-mini\",\"provider\":\"openai\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"},\"logprobs\":null,\"finish_reason\":\"stop\"}]}\n\n" +
			"data: [DONE]\n\n",
	)

	cached, ok := reconstructStreamingResponse("/v1/chat/completions", raw, streamResponseDefaults{
		Model:    "gpt-4o-mini",
		Provider: "openai",
	})
	if !ok {
		t.Fatal("expected streamed chat response to reconstruct successfully")
	}
	if !bytes.Contains(cached, []byte(`"logprobs":null`)) {
		t.Fatalf("reconstructed chat response = %q, want choice.logprobs preserved", string(cached))
	}

	replay, err := renderCachedChatStream([]byte(`{"model":"gpt-4o-mini","stream":true}`), cached)
	if err != nil {
		t.Fatalf("renderCachedChatStream() error = %v", err)
	}
	if !bytes.Contains(replay, []byte(`"logprobs":null`)) {
		t.Fatalf("cached chat replay = %q, want choice.logprobs preserved", string(replay))
	}
}

func TestReconstructStreamingResponse_PreservesResponsesReasoningText(t *testing.T) {
	raw := []byte(
		"event: response.created\n" +
			"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_reasoning_build\",\"object\":\"response\",\"created_at\":1234567890,\"model\":\"grok-4\",\"provider\":\"xai\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
			"event: response.output_item.added\n" +
			"data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"status\":\"in_progress\",\"summary\":[]}}\n\n" +
			"event: response.reasoning_text.delta\n" +
			"data: {\"type\":\"response.reasoning_text.delta\",\"item_id\":\"rs_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"step by\"}\n\n" +
			"event: response.reasoning_text.delta\n" +
			"data: {\"type\":\"response.reasoning_text.delta\",\"item_id\":\"rs_1\",\"output_index\":0,\"content_index\":1,\"delta\":\"step\"}\n\n" +
			"event: response.output_item.done\n" +
			"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"status\":\"completed\",\"summary\":[]}}\n\n" +
			"event: response.completed\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_reasoning_build\",\"object\":\"response\",\"created_at\":1234567890,\"model\":\"grok-4\",\"provider\":\"xai\",\"status\":\"completed\",\"output\":[{\"id\":\"rs_1\",\"type\":\"reasoning\",\"status\":\"completed\",\"summary\":[]}]}}\n\n" +
			"data: [DONE]\n\n",
	)

	cached, ok := reconstructStreamingResponse("/v1/responses", raw, streamResponseDefaults{
		Model:    "grok-4",
		Provider: "xai",
	})
	if !ok {
		t.Fatal("expected streamed responses payload to reconstruct successfully")
	}

	var response map[string]any
	if err := json.Unmarshal(cached, &response); err != nil {
		t.Fatalf("json.Unmarshal(cached) error = %v", err)
	}
	output, ok := response["output"].([]any)
	if !ok || len(output) != 1 {
		t.Fatalf("reconstructed output = %#v, want len=1", response["output"])
	}
	item, ok := output[0].(map[string]any)
	if !ok {
		t.Fatalf("reconstructed output[0] = %#v, want object", output[0])
	}
	if _, ok := item["content"]; ok {
		t.Fatalf("reasoning item should not use content field, got %#v", item["content"])
	}
	summary, ok := item["summary"].([]any)
	if !ok || len(summary) != 2 {
		t.Fatalf("reconstructed reasoning summary = %#v, want len=2", item["summary"])
	}
	wantTexts := []string{"step by", "step"}
	for i, wantText := range wantTexts {
		part, ok := summary[i].(map[string]any)
		if !ok {
			t.Fatalf("reconstructed summary[%d] = %#v, want object", i, summary[i])
		}
		if got, _ := part["type"].(string); got != "reasoning_text" {
			t.Fatalf("reconstructed summary part type = %q, want reasoning_text", got)
		}
		if got, _ := part["text"].(string); got != wantText {
			t.Fatalf("reconstructed summary part text = %q, want %q", got, wantText)
		}
	}

	replay, err := renderCachedResponsesStream([]byte(`{"model":"grok-4","stream":true}`), cached)
	if err != nil {
		t.Fatalf("renderCachedResponsesStream() error = %v", err)
	}
	var reasoningDeltas []map[string]any
	parseSSEJSONEvents(replay, func(event map[string]any) {
		if eventType, _ := event["type"].(string); eventType == "response.reasoning_text.delta" {
			reasoningDeltas = append(reasoningDeltas, event)
		}
	})
	if len(reasoningDeltas) != 2 {
		t.Fatalf("cached responses replay = %q, want 2 reasoning_text delta events", string(replay))
	}
	for i, deltaEvent := range reasoningDeltas {
		if got, _ := deltaEvent["delta"].(string); got != wantTexts[i] {
			t.Fatalf("reasoning delta text = %q, want %q", got, wantTexts[i])
		}
		if got, _ := deltaEvent["item_id"].(string); got != "rs_1" {
			t.Fatalf("reasoning delta item_id = %q, want rs_1", got)
		}
		if got, ok := jsonNumberToInt(deltaEvent["output_index"]); !ok || got != 0 {
			t.Fatalf("reasoning delta output_index = %#v, want 0", deltaEvent["output_index"])
		}
		if got, ok := jsonNumberToInt(deltaEvent["content_index"]); !ok || got != i {
			t.Fatalf("reasoning delta content_index = %#v, want %d", deltaEvent["content_index"], i)
		}
	}
}

func TestReconstructStreamingResponse_HonorsResponsesTextDeltaLocators(t *testing.T) {
	raw := []byte(
		"event: response.created\n" +
			"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_text_locator\",\"object\":\"response\",\"created_at\":1234567890,\"model\":\"gpt-4o-mini\",\"provider\":\"openai\",\"status\":\"in_progress\",\"output\":[{\"id\":\"rs_1\",\"type\":\"reasoning\",\"status\":\"in_progress\",\"summary\":[]}]}}\n\n" +
			"event: response.output_text.delta\n" +
			"data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":1,\"content_index\":0,\"delta\":\"final\"}\n\n" +
			"event: response.output_text.delta\n" +
			"data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":1,\"content_index\":1,\"delta\":\"answer\"}\n\n" +
			"event: response.completed\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_text_locator\",\"object\":\"response\",\"created_at\":1234567890,\"model\":\"gpt-4o-mini\",\"provider\":\"openai\",\"status\":\"completed\",\"output\":[{\"id\":\"rs_1\",\"type\":\"reasoning\",\"status\":\"completed\",\"summary\":[{\"type\":\"reasoning_text\",\"text\":\"step by step\"}]},{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[]}]}}\n\n" +
			"data: [DONE]\n\n",
	)

	cached, ok := reconstructStreamingResponse("/v1/responses", raw, streamResponseDefaults{
		Model:    "gpt-4o-mini",
		Provider: "openai",
	})
	if !ok {
		t.Fatal("expected streamed responses payload to reconstruct successfully")
	}

	var response map[string]any
	if err := json.Unmarshal(cached, &response); err != nil {
		t.Fatalf("json.Unmarshal(cached) error = %v", err)
	}
	output, ok := response["output"].([]any)
	if !ok || len(output) != 2 {
		t.Fatalf("reconstructed output = %#v, want len=2", response["output"])
	}
	reasoningItem, ok := output[0].(map[string]any)
	if !ok {
		t.Fatalf("reconstructed output[0] = %#v, want object", output[0])
	}
	if _, ok := reasoningItem["content"]; ok {
		t.Fatalf("reasoning item should not use content field, got %#v", reasoningItem["content"])
	}
	messageItem, ok := output[1].(map[string]any)
	if !ok {
		t.Fatalf("reconstructed output[1] = %#v, want object", output[1])
	}
	content, ok := messageItem["content"].([]any)
	if !ok || len(content) != 2 {
		t.Fatalf("reconstructed message content = %#v, want len=2", messageItem["content"])
	}
	wantTexts := []string{"final", "answer"}
	for i, wantText := range wantTexts {
		messagePart, ok := content[i].(map[string]any)
		if !ok {
			t.Fatalf("reconstructed message content[%d] = %#v, want object", i, content[i])
		}
		if got, _ := messagePart["text"].(string); got != wantText {
			t.Fatalf("reconstructed message text = %q, want %q", got, wantText)
		}
	}

	replay, err := renderCachedResponsesStream([]byte(`{"model":"gpt-4o-mini","stream":true}`), cached)
	if err != nil {
		t.Fatalf("renderCachedResponsesStream() error = %v", err)
	}
	var textDeltas []map[string]any
	parseSSEJSONEvents(replay, func(event map[string]any) {
		if eventType, _ := event["type"].(string); eventType == "response.output_text.delta" {
			textDeltas = append(textDeltas, event)
		}
	})
	if len(textDeltas) != 2 {
		t.Fatalf("cached responses replay = %q, want 2 output_text delta events", string(replay))
	}
	for i, deltaEvent := range textDeltas {
		if got, _ := deltaEvent["delta"].(string); got != wantTexts[i] {
			t.Fatalf("text delta text = %q, want %q", got, wantTexts[i])
		}
		if got, _ := deltaEvent["item_id"].(string); got != "msg_1" {
			t.Fatalf("text delta item_id = %q, want msg_1", got)
		}
		if got, ok := jsonNumberToInt(deltaEvent["output_index"]); !ok || got != 1 {
			t.Fatalf("text delta output_index = %#v, want 1", deltaEvent["output_index"])
		}
		if got, ok := jsonNumberToInt(deltaEvent["content_index"]); !ok || got != i {
			t.Fatalf("text delta content_index = %#v, want %d", deltaEvent["content_index"], i)
		}
	}
}

func TestRenderCachedResponsesStream_PreservesReasoningTextDeltas(t *testing.T) {
	cached := []byte(`{
		"id":"resp_reasoning",
		"object":"response",
		"created_at":1234567890,
		"model":"grok-4",
		"provider":"xai",
		"status":"completed",
		"output":[
			{
				"id":"rs_1",
				"type":"reasoning",
				"status":"completed",
				"summary":[{"type":"reasoning_text","text":"step by step"}]
			},
			{
				"id":"msg_1",
				"type":"message",
				"role":"assistant",
				"status":"completed",
				"content":[{"type":"output_text","text":"final answer"}]
			}
		]
	}`)

	replay, err := renderCachedResponsesStream([]byte(`{"model":"grok-4","stream":true}`), cached)
	if err != nil {
		t.Fatalf("renderCachedResponsesStream() error = %v", err)
	}
	if !bytes.Contains(replay, []byte("event: response.reasoning_text.delta")) {
		t.Fatalf("cached responses replay = %q, want reasoning_text delta event", string(replay))
	}
	if !bytes.Contains(replay, []byte("step by step")) {
		t.Fatalf("cached responses replay = %q, want reasoning delta text", string(replay))
	}
	if !bytes.Contains(replay, []byte("event: response.output_text.delta")) {
		t.Fatalf("cached responses replay = %q, want output_text delta event", string(replay))
	}
}

func TestRenderCachedResponsesStream_FunctionCallAddedItemOmitsArguments(t *testing.T) {
	cached := []byte(`{
		"id":"resp_function_call",
		"object":"response",
		"created_at":1234567890,
		"model":"gpt-4o-mini",
		"provider":"openai",
		"status":"completed",
		"output":[
			{
				"id":"fc_1",
				"type":"function_call",
				"status":"completed",
				"call_id":"call_1",
				"name":"lookup_weather",
				"arguments":"{\"city\":\"Warsaw\"}"
			}
		]
	}`)

	replay, err := renderCachedResponsesStream([]byte(`{"model":"gpt-4o-mini","stream":true}`), cached)
	if err != nil {
		t.Fatalf("renderCachedResponsesStream() error = %v", err)
	}

	var addedItem map[string]any
	var argDelta map[string]any
	var argDone map[string]any
	parseSSEJSONEvents(replay, func(event map[string]any) {
		switch eventType, _ := event["type"].(string); eventType {
		case "response.output_item.added":
			addedItem, _ = event["item"].(map[string]any)
		case "response.function_call_arguments.delta":
			argDelta = event
		case "response.function_call_arguments.done":
			argDone = event
		}
	})

	if addedItem == nil {
		t.Fatalf("cached responses replay = %q, want output_item.added event", string(replay))
	}
	if _, ok := addedItem["arguments"]; ok {
		t.Fatalf("added item arguments = %#v, want omitted", addedItem["arguments"])
	}
	if argDelta == nil || argDone == nil {
		t.Fatalf("cached responses replay = %q, want function_call_arguments delta and done events", string(replay))
	}
	if got, _ := argDelta["delta"].(string); got != `{"city":"Warsaw"}` {
		t.Fatalf("arguments delta = %q, want full arguments", got)
	}
	if got, _ := argDone["arguments"].(string); got != `{"city":"Warsaw"}` {
		t.Fatalf("arguments done = %q, want full arguments", got)
	}
}

func TestReconstructStreamingResponse_PreservesResponsesTerminalEvents(t *testing.T) {
	tests := []struct {
		name             string
		eventName        string
		status           string
		terminalResponse string
		assertTerminal   func(*testing.T, map[string]any)
	}{
		{
			name:             "failed",
			eventName:        "response.failed",
			status:           "failed",
			terminalResponse: `{"id":"resp_failed","object":"response","created_at":1234567890,"model":"gpt-4o-mini","provider":"openai","status":"failed","error":{"code":"boom","message":"upstream failed"},"metadata":{"trace":"abc"},"output":[]}`,
			assertTerminal: func(t *testing.T, response map[string]any) {
				t.Helper()
				errMap, ok := response["error"].(map[string]any)
				if !ok {
					t.Fatalf("terminal response error = %#v, want object", response["error"])
				}
				if got, _ := errMap["code"].(string); got != "boom" {
					t.Fatalf("terminal response error.code = %q, want boom", got)
				}
			},
		},
		{
			name:             "incomplete",
			eventName:        "response.incomplete",
			status:           "incomplete",
			terminalResponse: `{"id":"resp_incomplete","object":"response","created_at":1234567890,"model":"gpt-4o-mini","provider":"openai","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"metadata":{"trace":"def"},"output":[]}`,
			assertTerminal: func(t *testing.T, response map[string]any) {
				t.Helper()
				details, ok := response["incomplete_details"].(map[string]any)
				if !ok {
					t.Fatalf("terminal response incomplete_details = %#v, want object", response["incomplete_details"])
				}
				if got, _ := details["reason"].(string); got != "max_output_tokens" {
					t.Fatalf("terminal response incomplete_details.reason = %q, want max_output_tokens", got)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(
				"event: response.created\n" +
					"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_terminal\",\"object\":\"response\",\"created_at\":1234567890,\"model\":\"gpt-4o-mini\",\"provider\":\"openai\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
					"event: " + tt.eventName + "\n" +
					"data: {\"type\":\"" + tt.eventName + "\",\"response\":" + tt.terminalResponse + "}\n\n" +
					"data: [DONE]\n\n",
			)

			cached, ok := reconstructStreamingResponse("/v1/responses", raw, streamResponseDefaults{
				Model:    "gpt-4o-mini",
				Provider: "openai",
			})
			if !ok {
				t.Fatal("expected streamed responses payload to reconstruct successfully")
			}

			var cachedResponse map[string]any
			if err := json.Unmarshal(cached, &cachedResponse); err != nil {
				t.Fatalf("json.Unmarshal(cached) error = %v", err)
			}
			if got, _ := cachedResponse["status"].(string); got != tt.status {
				t.Fatalf("cached response status = %q, want %q", got, tt.status)
			}
			tt.assertTerminal(t, cachedResponse)

			replay, err := renderCachedResponsesStream([]byte(`{"model":"gpt-4o-mini","stream":true}`), cached)
			if err != nil {
				t.Fatalf("renderCachedResponsesStream() error = %v", err)
			}

			var terminalEvent map[string]any
			parseSSEJSONEvents(replay, func(event map[string]any) {
				if eventType, _ := event["type"].(string); eventType == tt.eventName {
					terminalEvent = event
				}
			})
			if terminalEvent == nil {
				t.Fatalf("cached responses replay = %q, want terminal event %s", string(replay), tt.eventName)
			}
			response, ok := terminalEvent["response"].(map[string]any)
			if !ok {
				t.Fatalf("terminal event response = %#v, want object", terminalEvent["response"])
			}
			if got, _ := response["status"].(string); got != tt.status {
				t.Fatalf("terminal event status = %q, want %q", got, tt.status)
			}
			tt.assertTerminal(t, response)
		})
	}
}
