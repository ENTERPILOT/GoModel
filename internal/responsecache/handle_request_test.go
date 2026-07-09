package responsecache

import (
	"bytes"
	"context"
	"errors"
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

func TestHandleInternalRequest_RejectsNilContext(t *testing.T) {
	m := NewResponseCacheMiddlewareWithStore(cache.NewMapStore(), time.Hour)
	var nilCtx context.Context

	_, err := m.HandleInternalRequest(nilCtx, http.MethodPost, "/v1/chat/completions", []byte(`{}`), func(context.Context) (*InternalResponse, error) {
		return &InternalResponse{StatusCode: http.StatusOK, ContentType: "application/json", Body: []byte(`{"ok":"1"}`)}, nil
	})
	if err == nil {
		t.Fatal("HandleInternalRequest() error = nil, want invalid request error")
	}

	gatewayErr, ok := err.(*core.GatewayError)
	if !ok {
		t.Fatalf("HandleInternalRequest() error = %T, want *core.GatewayError", err)
	}
	if gatewayErr.Type != core.ErrorTypeInvalidRequest {
		t.Fatalf("error type = %q, want %q", gatewayErr.Type, core.ErrorTypeInvalidRequest)
	}
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

	if got := headers.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept = %q, want application/json", got)
	}
	if got := headers.Get("User-Agent"); got != "gomodel-test" {
		t.Fatalf("User-Agent = %q, want gomodel-test", got)
	}
	if got := headers.Get("Traceparent"); got == "" {
		t.Fatal("Traceparent = empty, want preserved trace header")
	}
	if got := headers.Get("Baggage"); got != "user_id=123" {
		t.Fatalf("Baggage = %q, want user_id=123", got)
	}
	if got := headers.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := headers.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json default", got)
	}
	if got := headers.Get("X-Request-ID"); got != "req_123" {
		t.Fatalf("X-Request-ID = %q, want req_123", got)
	}
	if got := headers.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want omitted", got)
	}
	if got := headers.Get("Cookie"); got != "" {
		t.Fatalf("Cookie = %q, want omitted", got)
	}
	if got := headers.Get("X-Api-Key"); got != "" {
		t.Fatalf("X-Api-Key = %q, want omitted", got)
	}
}

func TestHandleInternalRequest_RejectsNilMiddleware(t *testing.T) {
	var m *ResponseCacheMiddleware

	_, err := m.HandleInternalRequest(context.Background(), http.MethodPost, "/v1/chat/completions", []byte(`{}`), func(context.Context) (*InternalResponse, error) {
		return &InternalResponse{StatusCode: http.StatusOK, ContentType: "application/json", Body: []byte(`{"ok":"1"}`)}, nil
	})
	if err == nil {
		t.Fatal("HandleInternalRequest() error = nil, want provider error")
	}

	gatewayErr, ok := err.(*core.GatewayError)
	if !ok {
		t.Fatalf("HandleInternalRequest() error = %T, want *core.GatewayError", err)
	}
	if gatewayErr.Type != core.ErrorTypeProvider {
		t.Fatalf("error type = %q, want %q", gatewayErr.Type, core.ErrorTypeProvider)
	}
	if gatewayErr.HTTPStatusCode() != http.StatusInternalServerError {
		t.Fatalf("status code = %d, want %d", gatewayErr.HTTPStatusCode(), http.StatusInternalServerError)
	}
}

func TestHandleInternalRequest_NormalizesNonGatewayErrors(t *testing.T) {
	m := NewResponseCacheMiddlewareWithStore(cache.NewMapStore(), time.Hour)
	originalErr := errors.New("cache executor failed")

	_, err := m.HandleInternalRequest(context.Background(), http.MethodPost, "/v1/chat/completions", []byte(`{}`), func(context.Context) (*InternalResponse, error) {
		return nil, originalErr
	})
	if err == nil {
		t.Fatal("HandleInternalRequest() error = nil, want provider error")
	}

	gatewayErr, ok := err.(*core.GatewayError)
	if !ok {
		t.Fatalf("HandleInternalRequest() error = %T, want *core.GatewayError", err)
	}
	if gatewayErr.Type != core.ErrorTypeProvider {
		t.Fatalf("error type = %q, want %q", gatewayErr.Type, core.ErrorTypeProvider)
	}
	if gatewayErr.Message != originalErr.Error() {
		t.Fatalf("message = %q, want %q", gatewayErr.Message, originalErr.Error())
	}
	if !errors.Is(gatewayErr, originalErr) {
		t.Fatal("expected wrapped gateway error to preserve original cause")
	}
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
	if err != nil {
		t.Fatalf("HandleInternalRequest() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("handler calls = %d, want 1", calls)
	}
	if result.StatusCode != http.StatusOK || string(result.Body) != `{"ok":"1"}` {
		t.Fatalf("result = %d %s, want 200 {\"ok\":\"1\"}", result.StatusCode, result.Body)
	}
	if result.CacheType != "" {
		t.Fatalf("CacheType = %q, want empty on a cacheless pass-through", result.CacheType)
	}
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
		if err != nil {
			t.Fatalf("HandleInternalRequest() error = %v", err)
		}
		return result
	}

	first := run()
	if first.CacheType != "" {
		t.Fatalf("first call CacheType = %q, want miss", first.CacheType)
	}
	if calls != 1 {
		t.Fatalf("handler calls = %d, want 1", calls)
	}
	m.simple.wg.Wait()

	second := run()
	if second.CacheType != CacheTypeExact {
		t.Fatalf("second call CacheType = %q, want %q", second.CacheType, CacheTypeExact)
	}
	if calls != 1 {
		t.Fatalf("exact hit must not call the handler again, calls = %d", calls)
	}
	if string(second.Body) != response {
		t.Fatalf("cached body = %s, want %s", second.Body, response)
	}
	if second.StatusCode != http.StatusOK {
		t.Fatalf("cached status = %d, want 200", second.StatusCode)
	}
	if got := second.Headers.Get("X-Cache"); got != CacheHeaderExact {
		t.Fatalf("X-Cache = %q, want %q", got, CacheHeaderExact)
	}

	// FailoverUsed responses must never be stored.
	failoverBody := []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"failover"}]}`)
	_, err := m.HandleInternalRequest(context.Background(), http.MethodPost, "/v1/chat/completions", failoverBody, func(context.Context) (*InternalResponse, error) {
		return &InternalResponse{StatusCode: http.StatusOK, ContentType: "application/json", Body: []byte(response), FailoverUsed: true}, nil
	})
	if err != nil {
		t.Fatalf("HandleInternalRequest() error = %v", err)
	}
	m.simple.wg.Wait()
	followUp, err := m.HandleInternalRequest(context.Background(), http.MethodPost, "/v1/chat/completions", failoverBody, func(context.Context) (*InternalResponse, error) {
		return &InternalResponse{StatusCode: http.StatusOK, ContentType: "application/json", Body: []byte(response)}, nil
	})
	if err != nil {
		t.Fatalf("HandleInternalRequest() error = %v", err)
	}
	if followUp.CacheType != "" {
		t.Fatalf("failover response was cached: CacheType = %q, want miss", followUp.CacheType)
	}
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
		if got := internalCacheType(tc.headerValue); got != tc.want {
			t.Fatalf("internalCacheType(%q) = %q, want %q", tc.headerValue, got, tc.want)
		}
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
	e := echo.New()
	handlerCalls := 0

	run := func(markFailover bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		if err := m.HandleRequest(c, body, func() error {
			handlerCalls++
			if markFailover {
				c.SetRequest(c.Request().WithContext(core.WithFailoverUsed(c.Request().Context())))
			}
			return c.JSON(http.StatusOK, map[string]string{"n": "1"})
		}); err != nil {
			t.Fatalf("HandleRequest: %v", err)
		}
		return rec
	}

	rec1 := run(true)
	if rec1.Header().Get("X-Cache") != "" {
		t.Fatalf("failover-served response should not be cached, got X-Cache=%q", rec1.Header().Get("X-Cache"))
	}
	if handlerCalls != 1 {
		t.Fatalf("expected 1 handler invocation after first request, got %d", handlerCalls)
	}

	m.simple.wg.Wait()
	m.semantic.wg.Wait()

	rec2 := run(false)
	if rec2.Header().Get("X-Cache") != "" {
		t.Fatalf("failover-served response should not populate cache, got X-Cache=%q", rec2.Header().Get("X-Cache"))
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
		plan := &core.Workflow{
			Mode:         core.ExecutionModeTranslated,
			ProviderType: "openai",
			Resolution: &core.RequestModelResolution{
				ResolvedSelector: core.ModelSelector{Provider: "openai", Model: "gpt-4"},
			},
		}
		c.SetRequest(req.WithContext(core.WithWorkflow(req.Context(), plan)))
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
	e := echo.New()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := auditlog.Middleware(logger)(func(c *echo.Context) error {
		return m.HandleRequest(c, body, func() error {
			return c.JSON(http.StatusGatewayTimeout, map[string]any{
				"error": map[string]any{
					"message": "provider timeout",
				},
			})
		})
	})

	if err := handler(c); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("response status = %d, want %d", rec.Code, http.StatusGatewayTimeout)
	}
	if len(logger.entries) != 1 {
		t.Fatalf("expected 1 audit log entry, got %d", len(logger.entries))
	}
	if got := logger.entries[0].StatusCode; got != http.StatusGatewayTimeout {
		t.Fatalf("audit status = %d, want %d", got, http.StatusGatewayTimeout)
	}
}

func TestHandleRequest_GatewayTimeoutDoesNotPopulateExactCache(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()

	m := &ResponseCacheMiddleware{
		simple: newSimpleCacheMiddleware(store, time.Hour, nil),
	}

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"do-not-cache-timeout"}]}`)
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
			return c.JSON(http.StatusGatewayTimeout, map[string]any{
				"error": map[string]any{
					"message": "timeout awaiting response headers",
				},
			})
		}); err != nil {
			t.Fatalf("HandleRequest: %v", err)
		}
		return rec
	}

	rec1 := run()
	if rec1.Code != http.StatusGatewayTimeout {
		t.Fatalf("first response status = %d, want %d", rec1.Code, http.StatusGatewayTimeout)
	}
	if got := rec1.Header().Get("X-Cache"); got != "" {
		t.Fatalf("first timeout response should not be cached, got X-Cache=%q", got)
	}

	m.simple.wg.Wait()

	rec2 := run()
	if rec2.Code != http.StatusGatewayTimeout {
		t.Fatalf("second response status = %d, want %d", rec2.Code, http.StatusGatewayTimeout)
	}
	if got := rec2.Header().Get("X-Cache"); got != "" {
		t.Fatalf("timeout response should not become an exact cache hit, got X-Cache=%q", got)
	}
	if handlerCalls != 2 {
		t.Fatalf("timeout response should execute handler twice, got %d calls", handlerCalls)
	}
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
	e := echo.New()
	handlerCalls := 0

	run := func() *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Cache-Type", CacheTypeSemantic)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		if err := m.HandleRequest(c, body, func() error {
			handlerCalls++
			return c.JSON(http.StatusGatewayTimeout, map[string]any{
				"error": map[string]any{
					"message": "timeout awaiting response headers",
				},
			})
		}); err != nil {
			t.Fatalf("HandleRequest: %v", err)
		}
		return rec
	}

	rec1 := run()
	if rec1.Code != http.StatusGatewayTimeout {
		t.Fatalf("first response status = %d, want %d", rec1.Code, http.StatusGatewayTimeout)
	}
	if got := rec1.Header().Get("X-Cache"); got != "" {
		t.Fatalf("first timeout response should not be cached semantically, got X-Cache=%q", got)
	}

	m.simple.wg.Wait()
	m.semantic.wg.Wait()

	rec2 := run()
	if rec2.Code != http.StatusGatewayTimeout {
		t.Fatalf("second response status = %d, want %d", rec2.Code, http.StatusGatewayTimeout)
	}
	if got := rec2.Header().Get("X-Cache"); got != "" {
		t.Fatalf("timeout response should not become a semantic cache hit, got X-Cache=%q", got)
	}
	if handlerCalls != 2 {
		t.Fatalf("timeout response should execute handler twice, got %d calls", handlerCalls)
	}
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
		plan := &core.Workflow{
			Mode:         core.ExecutionModeTranslated,
			ProviderType: "openai",
			Resolution: &core.RequestModelResolution{
				ResolvedSelector: core.ModelSelector{Provider: "openai", Model: "gpt-4"},
			},
		}
		c.SetRequest(req.WithContext(core.WithWorkflow(req.Context(), plan)))
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
		plan := &core.Workflow{
			Mode:         core.ExecutionModeTranslated,
			ProviderType: "openai",
			Resolution: &core.RequestModelResolution{
				ResolvedSelector: core.ModelSelector{Provider: "openai", Model: "gpt-4"},
			},
		}
		c.SetRequest(req.WithContext(core.WithWorkflow(req.Context(), plan)))
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
	e := echo.New()

	run := func() *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Request-ID", "req-cache-audit")
		plan := &core.Workflow{
			Mode:         core.ExecutionModeTranslated,
			ProviderType: "openai",
			Resolution: &core.RequestModelResolution{
				ResolvedSelector: core.ModelSelector{Provider: "openai", Model: "gpt-4"},
			},
		}
		req = req.WithContext(core.WithWorkflow(req.Context(), plan))
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
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
		if err := handler(c); err != nil {
			t.Fatalf("handler: %v", err)
		}
		return rec
	}

	rec1 := run()
	if got := rec1.Header().Get("X-Cache"); got != "" {
		t.Fatalf("first request should miss exact cache, got X-Cache=%q", got)
	}
	m.simple.wg.Wait()
	if len(logger.entries) != 0 {
		t.Fatalf("streaming miss test path should be handled by stream observer, got %d middleware entries", len(logger.entries))
	}

	rec2 := run()
	if got := rec2.Header().Get("X-Cache"); got != "HIT (exact)" {
		t.Fatalf("second request should be exact hit, got X-Cache=%q", got)
	}
	if len(logger.entries) != 1 {
		t.Fatalf("expected 1 audit log entry for cached stream hit, got %d", len(logger.entries))
	}
	entry := logger.entries[0]
	if !entry.Stream {
		t.Fatal("expected cached stream hit audit entry to be marked as streaming")
	}
	if entry.CacheType != auditlog.CacheTypeExact {
		t.Fatalf("CacheType = %q, want %q", entry.CacheType, auditlog.CacheTypeExact)
	}
	if entry.Data == nil || entry.Data.ResponseBody == nil {
		t.Fatalf("expected cached stream hit response body to be logged, got %#v", entry.Data)
	}
	response, ok := entry.Data.ResponseBody.(map[string]any)
	if !ok {
		t.Fatalf("response body type = %T, want map[string]any", entry.Data.ResponseBody)
	}
	choices, ok := response["choices"].([]map[string]any)
	if !ok || len(choices) != 1 {
		t.Fatalf("choices = %#v, want one choice", response["choices"])
	}
	message, ok := choices[0]["message"].(map[string]any)
	if !ok {
		t.Fatalf("message = %#v, want map[string]any", choices[0]["message"])
	}
	if got := message["content"]; got != "Hello cached audit" {
		t.Fatalf("logged response content = %#v, want %q", got, "Hello cached audit")
	}
	if got := entry.Data.ResponseHeaders["X-Cache"]; got != "HIT (exact)" {
		t.Fatalf("logged X-Cache header = %q, want HIT (exact)", got)
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
		plan := &core.Workflow{
			Mode:         core.ExecutionModeTranslated,
			ProviderType: "openai",
			Resolution: &core.RequestModelResolution{
				ResolvedSelector: core.ModelSelector{Provider: "openai", Model: "gpt-4"},
			},
		}
		c.SetRequest(req.WithContext(core.WithWorkflow(req.Context(), plan)))
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
