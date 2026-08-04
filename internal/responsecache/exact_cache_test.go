package responsecache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

type blockingMissExchange struct {
	ctx          context.Context
	started      chan<- struct{}
	release      <-chan struct{}
	joined       chan struct{}
	nonCacheable bool
	contextCalls atomic.Int32
}

// Context deliberately signals on StoreAfter's second Context call: the first
// reads the workflow and the second occurs after a follower joins the wait.
// Update this helper if StoreAfter's pre-wait call count changes.
func (e *blockingMissExchange) Context() context.Context {
	if e.joined != nil && e.contextCalls.Add(1) == 2 {
		close(e.joined)
	}
	return e.ctx
}
func (e *blockingMissExchange) Path() string                           { return "/v1/chat/completions" }
func (e *blockingMissExchange) Method() string                         { return http.MethodPost }
func (e *blockingMissExchange) RequestHeader(string) string            { return "" }
func (e *blockingMissExchange) MarkHit(string)                         {}
func (e *blockingMissExchange) ReplayHit([]byte, []byte, string) error { return nil }
func (e *blockingMissExchange) Capture(_ string, next func() error) ([]byte, bool, error) {
	if e.started != nil {
		e.started <- struct{}{}
	}
	if e.release != nil {
		<-e.release
	}
	if err := next(); err != nil {
		return nil, false, err
	}
	if e.nonCacheable {
		return nil, false, nil
	}
	return []byte(`{"ok":true}`), true, nil
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
	rec, err := driveHandleRequestResult(mw, workflow, body, headers, next)
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}
	return rec
}

func driveHandleRequestResult(
	mw *ResponseCacheMiddleware,
	workflow *core.Workflow,
	body []byte,
	headers map[string]string,
	next func(c *echo.Context) error,
) (*httptest.ResponseRecorder, error) {
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
	err := mw.HandleRequest(c, body, func() error { return next(c) })
	return rec, err
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

func TestStoreAfter_CoalescesConcurrentIdenticalMisses(t *testing.T) {
	m := newSimpleCacheMiddleware(cache.NewMapStore(), time.Hour, nil)
	defer m.close()
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"same"}]}`)
	var calls atomic.Int32
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	const requests = 12
	errs := make([]error, requests)
	var wg sync.WaitGroup
	wg.Go(func() {
		errs[0] = m.StoreAfter(&blockingMissExchange{
			ctx: context.Background(), started: started, release: release,
		}, body, func() error {
			calls.Add(1)
			return nil
		})
	})
	<-started

	joined := make([]chan struct{}, requests-1)
	for i := 1; i < requests; i++ {
		joined[i-1] = make(chan struct{})
		wg.Go(func() {
			errs[i] = m.StoreAfter(&blockingMissExchange{
				ctx: context.Background(), joined: joined[i-1],
			}, body, func() error {
				calls.Add(1)
				return nil
			})
		})
	}
	for _, waiter := range joined {
		<-waiter
	}
	close(release)
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Fatalf("provider calls = %d, want one coalesced miss", got)
	}
	for i := range requests {
		if errs[i] != nil {
			t.Fatalf("request %d: %v", i, errs[i])
		}
	}
}

func TestStoreAfter_CanceledFollowerDoesNotWaitForLeader(t *testing.T) {
	store := cache.NewMapStore()
	m := newSimpleCacheMiddleware(store, time.Hour, nil)
	defer m.close()
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"same"}]}`)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	leaderDone := make(chan error, 1)
	go func() {
		leaderDone <- m.StoreAfter(&blockingMissExchange{
			ctx: context.Background(), started: started, release: release,
		}, body, func() error { return nil })
	}()
	<-started

	followerCtx, cancel := context.WithCancel(context.Background())
	cancel()
	follower := &blockingMissExchange{ctx: followerCtx}
	if err := m.StoreAfter(follower, body, func() error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled follower error = %v, want context.Canceled", err)
	}
	close(release)
	if err := <-leaderDone; err != nil {
		t.Fatalf("leader error: %v", err)
	}
}

func TestStoreAfter_LeaderErrorIsNotFannedOut(t *testing.T) {
	m := newSimpleCacheMiddleware(cache.NewMapStore(), time.Hour, nil)
	defer m.close()
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"same"}]}`)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	leaderErr := errors.New("first provider failed")
	leaderDone := make(chan error, 1)
	go func() {
		leaderDone <- m.StoreAfter(&blockingMissExchange{
			ctx: context.Background(), started: started, release: release,
		}, body, func() error { return leaderErr })
	}()
	<-started

	const followers = 8
	joined := make([]chan struct{}, followers)
	errs := make([]error, followers)
	var followerCalls atomic.Int32
	var wg sync.WaitGroup
	for i := range followers {
		joined[i] = make(chan struct{})
		wg.Go(func() {
			errs[i] = m.StoreAfter(&blockingMissExchange{
				ctx: context.Background(), joined: joined[i],
			}, body, func() error {
				followerCalls.Add(1)
				return nil
			})
		})
	}
	for _, waiter := range joined {
		<-waiter
	}
	close(release)
	if err := <-leaderDone; !errors.Is(err, leaderErr) {
		t.Fatalf("leader error = %v, want %v", err, leaderErr)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("follower %d inherited leader error: %v", i, err)
		}
	}
	if got := followerCalls.Load(); got != followers {
		t.Fatalf("follower provider calls = %d, want %d independent retries", got, followers)
	}
}

func TestStoreAfter_CacheableFollowerStoresAfterNonCacheableLeader(t *testing.T) {
	store := cache.NewMapStore()
	m := newSimpleCacheMiddleware(store, time.Hour, nil)
	defer m.close()
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"same"}]}`)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	leaderDone := make(chan error, 1)
	go func() {
		leaderDone <- m.StoreAfter(&blockingMissExchange{
			ctx: context.Background(), started: started, release: release, nonCacheable: true,
		}, body, func() error { return nil })
	}()
	<-started

	joined := make(chan struct{})
	followerDone := make(chan error, 1)
	go func() {
		followerDone <- m.StoreAfter(&blockingMissExchange{
			ctx: context.Background(), joined: joined,
		}, body, func() error { return nil })
	}()
	<-joined
	close(release)
	if err := <-leaderDone; err != nil {
		t.Fatalf("leader error: %v", err)
	}
	if err := <-followerDone; err != nil {
		t.Fatalf("follower error: %v", err)
	}
	m.wg.Wait()
	key := hashRequest("/v1/chat/completions", body, nil)
	if cached, err := store.Get(context.Background(), key); err != nil || len(cached) == 0 {
		t.Fatalf("cached follower response = %q, err=%v", cached, err)
	}
}

func TestStoreAfter_LeaderPanicReleasesMiss(t *testing.T) {
	m := newSimpleCacheMiddleware(cache.NewMapStore(), time.Hour, nil)
	defer m.close()
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"same"}]}`)
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = m.StoreAfter(&blockingMissExchange{ctx: context.Background()}, body, func() error {
			panic("provider panic")
		})
	}()
	if recovered == nil {
		t.Fatal("leader panic did not propagate")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var calls atomic.Int32
	if err := m.StoreAfter(&blockingMissExchange{ctx: ctx}, body, func() error {
		calls.Add(1)
		return nil
	}); err != nil {
		t.Fatalf("request after leader panic: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("provider calls after leader panic = %d, want 1", got)
	}
}

func TestHashRequest_CanonicalizesJSONFormattingAndKeyOrder(t *testing.T) {
	plan := resolvedWorkflow("openai", "gpt-4")
	for _, tt := range []struct {
		name   string
		first  string
		second string
		equal  bool
	}{
		{name: "formatting and key order", first: `{"input":[1,2],"model":"gpt-4"}`, second: "{\n  \"model\": \"gpt-4\",\n  \"input\": [1, 2]\n}", equal: true},
		{name: "nested key order", first: `{"input":{"b":2,"a":1},"model":"gpt-4"}`, second: `{"model":"gpt-4","input":{"a":1,"b":2}}`, equal: true},
		{name: "number spelling preserved", first: `{"input":1,"model":"gpt-4"}`, second: `{"input":1.0,"model":"gpt-4"}`, equal: false},
		{name: "array order matters", first: `{"input":[1,2],"model":"gpt-4"}`, second: `{"input":[2,1],"model":"gpt-4"}`, equal: false},
		{name: "malformed falls back exactly", first: `{"input":`, second: ` {"input":`, equal: false},
		{name: "multiple values fall back exactly", first: `{"a":1} {"b":2}`, second: `{"a":1}  {"b":2}`, equal: false},
		{name: "duplicate names fall back exactly", first: `{"model":"a","model":"b"}`, second: `{"model":"b"}`, equal: false},
		{name: "nested duplicate names fall back exactly", first: `{"input":{"a":1,"a":2}}`, second: `{"input":{"a":2}}`, equal: false},
		{name: "oversized number before duplicate names", first: `{"n":1e1000000,"model":"a","model":"b"}`, second: `{"n":1e1000000,"model":"b"}`, equal: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			first := hashRequest("/v1/embeddings", []byte(tt.first), plan)
			second := hashRequest("/v1/embeddings", []byte(tt.second), plan)
			if got := first == second; got != tt.equal {
				t.Fatalf("key equality = %v, want %v: %s / %s", got, tt.equal, first, second)
			}
		})
	}
}

func TestHashRequest_DuplicateNamesDoNotCollideAfterTypedDecoding(t *testing.T) {
	plan := resolvedWorkflow("openai", "gpt-4")
	for _, tt := range []struct {
		path      string
		duplicate string
		collapsed string
	}{
		{path: "/v1/chat/completions", duplicate: `{"model":"a","model":"b","messages":[]}`, collapsed: `{"model":"b","messages":[]}`},
		{path: "/v1/responses", duplicate: `{"model":"a","model":"b","input":[]}`, collapsed: `{"model":"b","input":[]}`},
	} {
		t.Run(tt.path, func(t *testing.T) {
			first := hashRequest(tt.path, []byte(tt.duplicate), plan)
			second := hashRequest(tt.path, []byte(tt.collapsed), plan)
			if first == second {
				t.Fatal("duplicate-member request collided with its collapsed form")
			}
		})
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
