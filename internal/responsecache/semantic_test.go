package responsecache

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

	"gomodel/config"
	"gomodel/internal/auditlog"
	"gomodel/internal/core"
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

func intPtr(v int) *int    { return &v }
func boolPtr(v bool) *bool { return &v }

func newTestSemanticMiddleware(threshold float64, maxConvMessages int, excludeSystem bool) (*semanticCacheMiddleware, *MapVecStore, *mockEmbedder) {
	store := NewMapVecStore()
	emb := &mockEmbedder{vector: []float32{1, 0, 0}}
	cfg := config.SemanticCacheConfig{
		SimilarityThreshold:     threshold,
		TTL:                     intPtr(3600),
		MaxConversationMessages: intPtr(maxConvMessages),
		ExcludeSystemPrompt:     excludeSystem,
	}
	m := newSemanticCacheMiddleware(emb, store, cfg, nil)
	return m, store, emb
}

func serveSemanticRequest(t *testing.T, m *semanticCacheMiddleware, body []byte, guardrailsHash string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if guardrailsHash != "" {
		ctx := core.WithGuardrailsHash(req.Context(), guardrailsHash)
		req = req.WithContext(ctx)
	}
	c := e.NewContext(req, rec)
	err := m.Handle(c, body, func() error {
		return c.JSON(http.StatusOK, map[string]string{"answer": "42"})
	})
	if err != nil {
		t.Fatalf("Handle error: %v", err)
	}
	return rec
}

func TestSemanticCacheMiddleware_CacheHit(t *testing.T) {
	m, store, _ := newTestSemanticMiddleware(0.90, 10, false)
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"What is 2+2?"}]}`)

	rec1 := serveSemanticRequest(t, m, body, "")
	if rec1.Header().Get("X-Cache") != "" {
		t.Fatal("first request should be a miss")
	}
	m.wg.Wait()
	if store.Len() != 1 {
		t.Fatalf("expected 1 entry in store, got %d", store.Len())
	}

	rec2 := serveSemanticRequest(t, m, body, "")
	if rec2.Header().Get("X-Cache") != "HIT (semantic)" {
		t.Fatalf("second request should be a semantic hit, got X-Cache=%q", rec2.Header().Get("X-Cache"))
	}
}

func TestSemanticCacheMiddleware_ParaphraseFinalUserSharesNamespace(t *testing.T) {
	m, store, emb := newTestSemanticMiddleware(0.90, 10, false)
	emb.vector = []float32{1, 0, 0}

	body1 := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"What is 2+2?"}]}`)
	body2 := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"What's two plus two?"}]}`)

	serveSemanticRequest(t, m, body1, "")
	m.wg.Wait()
	if store.Len() != 1 {
		t.Fatalf("expected 1 entry, got %d", store.Len())
	}

	rec := serveSemanticRequest(t, m, body2, "")
	if rec.Header().Get("X-Cache") != "HIT (semantic)" {
		t.Fatalf("paraphrased last user text should share params namespace, got X-Cache=%q", rec.Header().Get("X-Cache"))
	}
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
	if store.Len() != 1 {
		t.Fatalf("expected 1 entry, got %d", store.Len())
	}

	rec := serveSemanticRequest(t, m, body2, "")
	if rec.Header().Get("X-Cache") != "HIT (semantic)" {
		t.Fatalf("multi-turn paraphrase of final user should hit, got X-Cache=%q", rec.Header().Get("X-Cache"))
	}
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
	if store.Len() != 1 {
		t.Fatalf("expected 1 entry, got %d", store.Len())
	}

	rec := serveSemanticRequest(t, m, body2, "")
	if rec.Header().Get("X-Cache") == "HIT (semantic)" {
		t.Fatal("different non-text attachment should not share semantic namespace")
	}
}

func TestSemanticCacheMiddleware_CacheMissOnLowScore(t *testing.T) {
	store := NewMapVecStore()
	emb := &mockEmbedder{}

	m := newSemanticCacheMiddleware(emb, store, config.SemanticCacheConfig{
		SimilarityThreshold:     0.99,
		TTL:                     intPtr(3600),
		MaxConversationMessages: intPtr(10),
	}, nil)

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`)

	emb.vector = []float32{1, 0, 0}
	serveSemanticRequest(t, m, body, "")
	m.wg.Wait()

	emb.vector = []float32{0, 1, 0}
	rec := serveSemanticRequest(t, m, body, "")
	if rec.Header().Get("X-Cache") == "HIT (semantic)" {
		t.Fatal("orthogonal vector should not hit cache with high threshold")
	}
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
	if store.Len() != 1 {
		t.Fatal("expected one entry after first insert")
	}

	rec := serveSemanticRequest(t, m, body2, "")
	if rec.Header().Get("X-Cache") == "HIT (semantic)" {
		t.Fatal("different temperature should produce different params_hash → cache miss")
	}
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

	if first == second {
		t.Fatal("stream_options.include_usage should affect semantic params_hash")
	}
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

	if first == second {
		t.Fatal("stream mode should affect semantic params_hash")
	}
}

func TestSemanticCacheMiddleware_GuardrailsHashIsolation(t *testing.T) {
	m, store, emb := newTestSemanticMiddleware(0.90, 10, false)
	emb.vector = []float32{1, 0, 0}

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"medical question"}]}`)

	serveSemanticRequest(t, m, body, "guardrails-v1-hash")
	m.wg.Wait()
	if store.Len() != 1 {
		t.Fatal("expected one entry after insert under guardrail v1")
	}

	rec := serveSemanticRequest(t, m, body, "guardrails-v2-hash")
	if rec.Header().Get("X-Cache") == "HIT (semantic)" {
		t.Fatal("changed guardrails_hash should produce different params_hash → cache miss")
	}
}

func TestSemanticCacheMiddleware_DynamicGuardrailsIsolation(t *testing.T) {
	m, store, emb := newTestSemanticMiddleware(0.90, 10, false)
	emb.vector = []float32{1, 0, 0}

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"user PII data"}]}`)

	serveSemanticRequest(t, m, body, "hash-user-a-pii-rule")
	m.wg.Wait()
	if store.Len() != 1 {
		t.Fatal("expected one entry after insert for user A")
	}

	rec := serveSemanticRequest(t, m, body, "hash-user-b-different-pii-rule")
	if rec.Header().Get("X-Cache") == "HIT (semantic)" {
		t.Fatal("per-user dynamic guardrails hash difference should isolate cache entries")
	}
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

	if store.Len() != 0 {
		t.Fatal("conversation exceeding MaxConversationMessages should skip semantic caching")
	}
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
	if store.Len() != 1 {
		t.Fatal("exclude_system_prompt=true: should still cache user message")
	}

	rec := serveSemanticRequest(t, m, body, "")
	if rec.Header().Get("X-Cache") != "HIT (semantic)" {
		t.Fatal("same user message with system excluded should hit cache")
	}
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
	e := echo.New()
	handlerCalls := 0

	run := func(body []byte) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		if err := m.Handle(c, body, func() error {
			handlerCalls++
			if isStreamingRequest(c.Request().URL.Path, body) {
				c.Response().Header().Set("Content-Type", "text/event-stream")
				c.Response().WriteHeader(http.StatusOK)
				_, _ = c.Response().Write(rawStream)
				return nil
			}
			return c.JSON(http.StatusOK, map[string]string{"mode": "json"})
		}); err != nil {
			t.Fatalf("Handle error: %v", err)
		}
		return rec
	}

	rec1 := run(streamBody)
	if got := rec1.Header().Get("X-Cache"); got != "" {
		t.Fatalf("streaming miss should not be cache hit, got X-Cache=%q", got)
	}

	m.wg.Wait()

	if store.Len() != 1 {
		t.Fatalf("expected streaming miss to populate semantic cache, got %d entries", store.Len())
	}

	rec2 := run(jsonBody)
	if got := rec2.Header().Get("X-Cache"); got != "" {
		t.Fatalf("non-streaming follow-up should miss semantic cache because stream mode is keyed separately, got X-Cache=%q", got)
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

	m.wg.Wait()

	if store.Len() != 2 {
		t.Fatalf("expected separate semantic entries for stream and non-stream requests, got %d entries", store.Len())
	}

	rec3 := run(streamBody)
	if got := rec3.Header().Get("X-Cache"); got != "HIT (semantic)" {
		t.Fatalf("streaming follow-up should hit its own semantic cache entry, got X-Cache=%q", got)
	}
	if got := rec3.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("streaming semantic hit Content-Type = %q, want text/event-stream", got)
	}
	if !bytes.Equal(rec3.Body.Bytes(), rawStream) {
		t.Fatalf("streaming semantic hit body = %q, want verbatim SSE replay", rec3.Body.String())
	}
	if handlerCalls != 2 {
		t.Fatalf("streaming semantic hit should not call handler again, got %d calls", handlerCalls)
	}

	rec4 := run(jsonBody)
	if got := rec4.Header().Get("X-Cache"); got != "HIT (semantic)" {
		t.Fatalf("non-streaming follow-up should hit its own semantic cache entry, got X-Cache=%q", got)
	}
	if got := rec4.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("non-streaming semantic hit Content-Type = %q, want application/json", got)
	}
	if !bytes.Contains(rec4.Body.Bytes(), []byte(`"mode":"json"`)) {
		t.Fatalf("non-streaming semantic hit body = %q, want cached JSON response", rec4.Body.String())
	}
	if handlerCalls != 2 {
		t.Fatalf("non-streaming semantic hit should not call handler again, got %d calls", handlerCalls)
	}
}

func TestSemanticCacheMiddleware_InvalidStreamingBodySkipsSemanticCacheWrite(t *testing.T) {
	m, store, _ := newTestSemanticMiddleware(0.90, 10, false)
	body := []byte(`{"model":"gpt-4","stream":true,"messages":[{"role":"user","content":"semantic-invalid-stream"}]}`)
	invalidStream := []byte(
		": keep-alive\n\n" +
			"data: {\"id\":\"chatcmpl-semantic-invalid\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n",
	)
	e := echo.New()
	handlerCalls := 0

	run := func() *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		if err := m.Handle(c, body, func() error {
			handlerCalls++
			c.Response().Header().Set("Content-Type", "text/event-stream")
			c.Response().WriteHeader(http.StatusOK)
			_, _ = c.Response().Write(invalidStream)
			return nil
		}); err != nil {
			t.Fatalf("Handle error: %v", err)
		}
		return rec
	}

	rec1 := run()
	if got := rec1.Header().Get("X-Cache"); got != "" {
		t.Fatalf("first request should miss cache, got X-Cache=%q", got)
	}

	m.wg.Wait()

	if store.Len() != 0 {
		t.Fatalf("invalid streaming body should not populate semantic cache, got %d entries", store.Len())
	}

	rec2 := run()
	if got := rec2.Header().Get("X-Cache"); got != "" {
		t.Fatalf("invalid streaming body should not be cached, got X-Cache=%q", got)
	}
	if handlerCalls != 2 {
		t.Fatalf("expected invalid stream to bypass semantic cache on follow-up, got %d calls", handlerCalls)
	}
}

func TestSemanticCacheMiddleware_NoCacheControlSkip(t *testing.T) {
	m, store, _ := newTestSemanticMiddleware(0.90, 10, false)

	e := echo.New()
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"no-store test"}]}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Cache-Control", "no-store")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handlerCalled := false
	err := m.Handle(c, body, func() error {
		handlerCalled = true
		return c.JSON(http.StatusOK, map[string]string{"r": "1"})
	})
	if err != nil {
		t.Fatalf("Handle error: %v", err)
	}
	m.wg.Wait()

	if store.Len() != 0 {
		t.Fatal("no-store should skip semantic caching")
	}
	if !handlerCalled {
		t.Fatal("handler should still be called on no-store")
	}
}

func TestSemanticCacheMiddleware_HeaderThresholdOverride(t *testing.T) {
	store := NewMapVecStore()
	emb := &mockEmbedder{}

	m := newSemanticCacheMiddleware(emb, store, config.SemanticCacheConfig{
		SimilarityThreshold:     0.99,
		TTL:                     intPtr(3600),
		MaxConversationMessages: intPtr(10),
	}, nil)

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`)

	emb.vector = []float32{1, 0, 0}
	serveSemanticRequest(t, m, body, "")
	m.wg.Wait()

	emb.vector = []float32{0.95, 0.05, 0}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("X-Cache-Semantic-Threshold", "0.50")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := m.Handle(c, body, func() error {
		return c.JSON(http.StatusOK, map[string]string{"r": "1"})
	}); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if rec.Header().Get("X-Cache") != "HIT (semantic)" {
		t.Fatalf("lowered threshold via header should hit cache, got X-Cache=%q", rec.Header().Get("X-Cache"))
	}
}

func TestSemanticCacheMiddleware_TTLExpiry(t *testing.T) {
	store := NewMapVecStore()
	emb := &mockEmbedder{vector: []float32{1, 0, 0}}

	m := newSemanticCacheMiddleware(emb, store, config.SemanticCacheConfig{
		SimilarityThreshold:     0.90,
		TTL:                     intPtr(1),
		MaxConversationMessages: intPtr(10),
	}, nil)

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"expiry test"}]}`)
	serveSemanticRequest(t, m, body, "")
	m.wg.Wait()

	if store.Len() != 1 {
		t.Fatal("expected one entry after insert")
	}

	time.Sleep(2 * time.Second)

	if err := store.DeleteExpired(context.Background()); err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if store.Len() != 0 {
		t.Fatal("expected expired entry to be removed")
	}
}

func TestMapVecStore_DeleteExpiredOnlyRemovesExpired(t *testing.T) {
	store := NewMapVecStore()
	ctx := context.Background()

	_ = store.Insert(ctx, "key-expired", []float32{1, 0}, nil, "ph", -1*time.Second)
	_ = store.Insert(ctx, "key-live", []float32{0, 1}, nil, "ph", time.Hour)

	_ = store.DeleteExpired(ctx)

	if store.Len() != 1 {
		t.Fatalf("expected 1 live entry, got %d", store.Len())
	}
	results, _ := store.Search(ctx, []float32{0, 1}, "ph", 1)
	if len(results) == 0 || results[0].Key != "key-live" {
		t.Fatal("live entry should still be searchable")
	}
}

func TestComputeGuardrailsHash_Stable(t *testing.T) {
	rules := []GuardrailRuleDescriptor{
		{Name: "safety", Type: "system_prompt", Order: 0, Mode: "", Content: "Be safe."},
		{Name: "privacy", Type: "system_prompt", Order: 0, Mode: "", Content: "No PII."},
	}
	h1 := ComputeGuardrailsHash(rules)
	h2 := ComputeGuardrailsHash(rules)
	if h1 != h2 {
		t.Fatal("hash should be stable across calls")
	}
}

func TestComputeGuardrailsHash_OrderIndependent(t *testing.T) {
	rules1 := []GuardrailRuleDescriptor{
		{Name: "safety", Type: "system_prompt", Order: 0, Mode: "", Content: "Be safe."},
		{Name: "privacy", Type: "system_prompt", Order: 0, Mode: "", Content: "No PII."},
	}
	rules2 := []GuardrailRuleDescriptor{
		{Name: "privacy", Type: "system_prompt", Order: 0, Mode: "", Content: "No PII."},
		{Name: "safety", Type: "system_prompt", Order: 0, Mode: "", Content: "Be safe."},
	}
	if ComputeGuardrailsHash(rules1) != ComputeGuardrailsHash(rules2) {
		t.Fatal("hash should be order-independent (rules are sorted)")
	}
}

func TestComputeGuardrailsHash_ChangesOnContentChange(t *testing.T) {
	v1 := []GuardrailRuleDescriptor{{Name: "safety", Type: "system_prompt", Order: 0, Mode: "", Content: "Be safe."}}
	v2 := []GuardrailRuleDescriptor{{Name: "safety", Type: "system_prompt", Order: 0, Mode: "", Content: "Be very safe."}}
	if ComputeGuardrailsHash(v1) == ComputeGuardrailsHash(v2) {
		t.Fatal("hash should change when rule content changes")
	}
}

func TestComputeGuardrailsHash_ChangesOnRuleOrderOrMode(t *testing.T) {
	base := []GuardrailRuleDescriptor{{Name: "safety", Type: "system_prompt", Order: 0, Mode: "inject", Content: "Be safe."}}
	reordered := []GuardrailRuleDescriptor{{Name: "safety", Type: "system_prompt", Order: 1, Mode: "inject", Content: "Be safe."}}
	mode := []GuardrailRuleDescriptor{{Name: "safety", Type: "system_prompt", Order: 0, Mode: "override", Content: "Be safe."}}
	if ComputeGuardrailsHash(base) == ComputeGuardrailsHash(reordered) {
		t.Fatal("hash should change when guardrail execution order changes")
	}
	if ComputeGuardrailsHash(base) == ComputeGuardrailsHash(mode) {
		t.Fatal("hash should change when system_prompt mode changes")
	}
}

func TestShouldSkipAllCache_CacheControlNoStore(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Cache-Control", "private, no-store, max-age=0")
	if !ShouldSkipAllCache(req) {
		t.Fatal("expected ShouldSkipAllCache for Cache-Control: no-store")
	}
}

func TestShouldSkipAllCache_CacheControlNoCache(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Cache-Control", "private, no-cache, max-age=0")
	if !ShouldSkipAllCache(req) {
		t.Fatal("expected ShouldSkipAllCache for Cache-Control: no-cache")
	}
}

func TestSemanticCacheMiddleware_HitMarksAuditEntryCacheType(t *testing.T) {
	m, _, _ := newTestSemanticMiddleware(0.90, 10, false)
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"semantic-cache-type"}]}`)

	rec1 := serveSemanticRequest(t, m, body, "")
	if rec1.Header().Get("X-Cache") != "" {
		t.Fatalf("first request should miss semantic cache, got X-Cache=%q", rec1.Header().Get("X-Cache"))
	}
	m.wg.Wait()

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	entry := &auditlog.LogEntry{ID: "semantic-audit-entry"}
	c.Set(string(auditlog.LogEntryKey), entry)

	if err := m.Handle(c, body, func() error {
		return c.JSON(http.StatusOK, map[string]string{"answer": "42"})
	}); err != nil {
		t.Fatalf("Handle error: %v", err)
	}

	if rec.Header().Get("X-Cache") != "HIT (semantic)" {
		t.Fatalf("expected semantic cache hit, got X-Cache=%q", rec.Header().Get("X-Cache"))
	}
	if entry.CacheType != auditlog.CacheTypeSemantic {
		t.Fatalf("CacheType = %q, want %q", entry.CacheType, auditlog.CacheTypeSemantic)
	}
}
