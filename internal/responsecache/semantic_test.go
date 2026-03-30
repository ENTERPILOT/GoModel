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

func newTestSemanticMiddleware(threshold float64, maxConvMessages int, excludeSystem bool) (*semanticCacheMiddleware, *MapVecStore, *mockEmbedder) {
	store := NewMapVecStore()
	emb := &mockEmbedder{vector: []float32{1, 0, 0}}
	cfg := config.SemanticCacheConfig{
		Enabled:                 true,
		SimilarityThreshold:     threshold,
		TTL:                     3600,
		MaxConversationMessages: maxConvMessages,
		ExcludeSystemPrompt:     excludeSystem,
	}
	m := newSemanticCacheMiddleware(emb, store, cfg)
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
		Enabled:                 true,
		SimilarityThreshold:     0.99,
		TTL:                     3600,
		MaxConversationMessages: 10,
	})

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

func TestSemanticCacheMiddleware_StreamingSkipped(t *testing.T) {
	m, store, _ := newTestSemanticMiddleware(0.90, 10, false)

	body := []byte(`{"model":"gpt-4","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	serveSemanticRequest(t, m, body, "")
	m.wg.Wait()

	if store.Len() != 0 {
		t.Fatal("streaming requests should be skipped by semantic cache")
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
		Enabled:                 true,
		SimilarityThreshold:     0.99,
		TTL:                     3600,
		MaxConversationMessages: 10,
	})

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
		Enabled:                 true,
		SimilarityThreshold:     0.90,
		TTL:                     1,
		MaxConversationMessages: 10,
	})

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
