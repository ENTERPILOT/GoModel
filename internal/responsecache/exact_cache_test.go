package responsecache

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/cache"
	"github.com/enterpilot/gomodel/internal/core"
)

var benchmarkStreamingBody = []byte(`{"model":"gpt-4","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

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

func resolvedWorkflow(providerType, model string) *core.Workflow {
	desc := core.DescribeEndpoint(http.MethodPost, "/v1/chat/completions")
	return &core.Workflow{
		Endpoint:     desc,
		Mode:         core.ExecutionModeTranslated,
		Capabilities: core.CapabilitiesForEndpoint(desc),
		ProviderType: providerType,
		Resolution: &core.RequestModelResolution{
			Requested:        core.NewRequestedModelSelector(model, providerType),
			ResolvedSelector: core.ModelSelector{Provider: providerType, Model: model},
			ProviderType:     providerType,
		},
	}
}

// driveHandleRequest exercises the production cache entry the way the
// translated inference service does: workflow on the request context, the
// patched body passed explicitly, and next writing the LLM response through
// the echo context.
func driveHandleRequest(
	t *testing.T,
	mw *ResponseCacheMiddleware,
	workflow *core.Workflow,
	body []byte,
	headers map[string]string,
	next func(c *echo.Context) error,
) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	if workflow != nil {
		req = req.WithContext(core.WithWorkflow(req.Context(), workflow))
	}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if err := mw.HandleRequest(c, body, func() error { return next(c) }); err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}
	return rec
}

func TestHandleRequest_ExactCacheHit(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()
	mw := NewResponseCacheMiddlewareWithStore(store, time.Hour)
	workflow := resolvedWorkflow("openai", "gpt-4")
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	callCount := 0
	next := func(c *echo.Context) error {
		callCount++
		return c.JSON(http.StatusOK, map[string]string{"result": "cached"})
	}

	rec := driveHandleRequest(t, mw, workflow, body, nil, next)
	if rec.Code != http.StatusOK {
		t.Fatalf("first request: got status %d", rec.Code)
	}
	if rec.Header().Get("X-Cache") != "" {
		t.Fatalf("first request should not have X-Cache: %s", rec.Header().Get("X-Cache"))
	}

	// Wait for the tracked background write to complete before the second request.
	mw.simple.wg.Wait()

	rec2 := driveHandleRequest(t, mw, workflow, body, nil, next)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second request: got status %d", rec2.Code)
	}
	if rec2.Header().Get("X-Cache") != "HIT (exact)" {
		t.Fatalf("second request should have X-Cache=HIT (exact), got %s", rec2.Header().Get("X-Cache"))
	}
	if !bytes.Contains(rec2.Body.Bytes(), []byte("cached")) {
		t.Fatalf("cached response body missing expected content: %s", rec2.Body.String())
	}
	if callCount != 1 {
		t.Fatalf("exact hit should not call next again, callCount=%d", callCount)
	}
}

func TestHandleRequest_DifferentBodyDifferentKey(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()
	mw := NewResponseCacheMiddlewareWithStore(store, time.Hour)
	workflow := resolvedWorkflow("openai", "gpt-4")
	next := func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"msg": "fresh"})
	}

	body1 := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	body2 := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"bye"}]}`)

	rec1 := driveHandleRequest(t, mw, workflow, body1, nil, next)
	if rec1.Header().Get("X-Cache") != "" {
		t.Fatal("first request should miss")
	}
	mw.simple.wg.Wait()

	rec2 := driveHandleRequest(t, mw, workflow, body2, nil, next)
	if rec2.Header().Get("X-Cache") != "" {
		t.Fatal("different body should miss cache")
	}
}

func TestHashRequest_ResolvedModelChangesKey(t *testing.T) {
	body := []byte(`{"model":"anthropic/claude-opus-4-6","messages":[{"role":"user","content":"hi"}]}`)

	first := hashRequest("/v1/chat/completions", body, &core.Workflow{
		Mode: core.ExecutionModeTranslated,
		Resolution: &core.RequestModelResolution{
			ResolvedSelector: core.ModelSelector{Provider: "openai", Model: "gpt-5-nano"},
		},
	})
	second := hashRequest("/v1/chat/completions", body, &core.Workflow{
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

	first := hashRequest("/v1/chat/completions", body, &core.Workflow{
		Mode: core.ExecutionModeTranslated,
	})
	second := hashRequest("/v1/chat/completions", body, &core.Workflow{
		Mode: core.ExecutionModePassthrough,
	})

	if first == second {
		t.Fatal("execution mode should affect cache key")
	}
}

func TestHashRequest_StreamIncludeUsageChangesKey(t *testing.T) {
	base := []byte(`{"model":"gpt-4","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	withUsage := []byte(`{"model":"gpt-4","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`)
	plan := &core.Workflow{
		Mode:         core.ExecutionModeTranslated,
		ProviderType: "openai",
		Resolution: &core.RequestModelResolution{
			ResolvedSelector: core.ModelSelector{Provider: "openai", Model: "gpt-4"},
		},
	}

	first := hashRequest("/v1/chat/completions", base, plan)
	second := hashRequest("/v1/chat/completions", withUsage, plan)

	if first == second {
		t.Fatal("stream_options.include_usage should affect the exact cache key")
	}
}

func TestHashRequest_StreamModeChangesKey(t *testing.T) {
	base := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	streaming := []byte(`{"model":"gpt-4","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	plan := &core.Workflow{
		Mode:         core.ExecutionModeTranslated,
		ProviderType: "openai",
		Resolution: &core.RequestModelResolution{
			ResolvedSelector: core.ModelSelector{Provider: "openai", Model: "gpt-4"},
		},
	}

	first := hashRequest("/v1/chat/completions", base, plan)
	second := hashRequest("/v1/chat/completions", streaming, plan)

	if first == second {
		t.Fatal("stream mode should affect the exact cache key")
	}
}

func TestHandleRequest_SeparatesStreamingAndNonStreamingEntries(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()
	mw := NewResponseCacheMiddlewareWithStore(store, time.Hour)
	workflow := resolvedWorkflow("openai", "gpt-4")
	callCount := 0
	rawStream := []byte(
		"data: {\"id\":\"chatcmpl-stream\",\"object\":\"chat.completion.chunk\",\"created\":1234567890,\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"streamed\"},\"finish_reason\":null}]}\n\n" +
			"data: {\"id\":\"chatcmpl-stream\",\"object\":\"chat.completion.chunk\",\"created\":1234567890,\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":1,\"total_tokens\":10}}\n\n" +
			"data: [DONE]\n\n",
	)
	makeNext := func(body []byte) func(c *echo.Context) error {
		return func(c *echo.Context) error {
			callCount++
			if isStreamingRequest(c.Request().URL.Path, body) {
				c.Response().Header().Set("Content-Type", "text/event-stream")
				c.Response().WriteHeader(http.StatusOK)
				_, _ = c.Response().Write(rawStream)
				return nil
			}
			return c.JSON(http.StatusOK, map[string]string{"result": "json cached response"})
		}
	}

	nonStreamingBody := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	streamingBody := []byte(`{"model":"gpt-4","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	rec1 := driveHandleRequest(t, mw, workflow, nonStreamingBody, nil, makeNext(nonStreamingBody))
	if rec1.Header().Get("X-Cache") != "" {
		t.Fatalf("first request should miss cache, got X-Cache=%q", rec1.Header().Get("X-Cache"))
	}

	mw.simple.wg.Wait()

	rec2 := driveHandleRequest(t, mw, workflow, streamingBody, nil, makeNext(streamingBody))
	if got := rec2.Header().Get("X-Cache"); got != "" {
		t.Fatalf("streaming request should miss exact cache because stream mode is keyed separately, got X-Cache=%q", got)
	}
	if got := rec2.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("streaming miss Content-Type = %q, want text/event-stream", got)
	}
	if !bytes.Equal(rec2.Body.Bytes(), rawStream) {
		t.Fatalf("streaming miss body = %q, want original SSE payload", rec2.Body.String())
	}
	if callCount != 2 {
		t.Fatalf("expected separate stream miss to call handler again, got %d calls", callCount)
	}

	mw.simple.wg.Wait()

	rec3 := driveHandleRequest(t, mw, workflow, streamingBody, nil, makeNext(streamingBody))
	if got := rec3.Header().Get("X-Cache"); got != "HIT (exact)" {
		t.Fatalf("streaming follow-up should hit its own exact cache entry, got X-Cache=%q", got)
	}
	if got := rec3.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("streaming cache hit Content-Type = %q, want text/event-stream", got)
	}
	if !bytes.Equal(rec3.Body.Bytes(), rawStream) {
		t.Fatalf("streaming cache hit body = %q, want verbatim SSE replay", rec3.Body.String())
	}
	if callCount != 2 {
		t.Fatalf("expected streaming replay to avoid a third handler call, got %d calls", callCount)
	}

	rec4 := driveHandleRequest(t, mw, workflow, nonStreamingBody, nil, makeNext(nonStreamingBody))
	if got := rec4.Header().Get("X-Cache"); got != "HIT (exact)" {
		t.Fatalf("non-streaming follow-up should hit its own exact cache entry, got X-Cache=%q", got)
	}
	if got := rec4.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("non-streaming cache hit Content-Type = %q, want application/json", got)
	}
	if !bytes.Contains(rec4.Body.Bytes(), []byte("json cached response")) {
		t.Fatalf("non-streaming cache hit body = %q, want cached JSON response", rec4.Body.String())
	}
	if callCount != 2 {
		t.Fatalf("non-streaming exact hit should not call handler again, got %d calls", callCount)
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

func TestHandleRequest_SkipsNoCache(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()
	mw := NewResponseCacheMiddlewareWithStore(store, time.Hour)
	workflow := resolvedWorkflow("openai", "gpt-4")
	callCount := 0
	next := func(c *echo.Context) error {
		callCount++
		return c.JSON(http.StatusOK, map[string]string{"n": "1"})
	}
	headers := map[string]string{"Cache-Control": "no-cache"}

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	for range 2 {
		rec := driveHandleRequest(t, mw, workflow, body, headers, next)
		if got := rec.Header().Get("X-Cache"); got != "" {
			t.Fatalf("no-cache request should bypass cache, got X-Cache=%q", got)
		}
	}
	if callCount != 2 {
		t.Fatalf("no-cache requests should bypass cache, handler called %d times", callCount)
	}
}

func TestClose_WaitsForPendingWrites(t *testing.T) {
	store := cache.NewMapStore()
	mw := NewResponseCacheMiddlewareWithStore(store, time.Hour)
	workflow := resolvedWorkflow("openai", "gpt-4")

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"close-test"}]}`)
	rec := driveHandleRequest(t, mw, workflow, body, nil, func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"result": "ok"})
	})
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

func TestLimitsConcurrentCacheWrites(t *testing.T) {
	store := newConcurrentTrackingStore()
	mw := NewResponseCacheMiddlewareWithStore(store, time.Hour)
	workflow := resolvedWorkflow("openai", "gpt-4")

	const requestCount = cacheWriteWorkerCount * 2

	var reqWG sync.WaitGroup
	for i := range requestCount {
		body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi ` + string(rune('a'+i)) + `"}]}`)
		reqWG.Go(func() {
			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req = req.WithContext(core.WithWorkflow(req.Context(), workflow))
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			err := mw.HandleRequest(c, body, func() error {
				return c.JSON(http.StatusOK, map[string]string{"result": "ok"})
			})
			if err != nil {
				t.Errorf("HandleRequest: %v", err)
				return
			}
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
