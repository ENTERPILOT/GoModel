package responsecache

import (
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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/cache"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
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

// postWithWorkflow builds the request the inference service hands to the
// cache: a JSON POST carrying the resolved workflow on its context.
func postWithWorkflow(t *testing.T, target string, body []byte, workflow *core.Workflow, opts ...echotest.Option) (*echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	c, rec := echotest.Post(t, target, body, opts...)
	if workflow != nil {
		req := c.Request()
		c.SetRequest(req.WithContext(core.WithWorkflow(req.Context(), workflow)))
	}
	return c, rec
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
	opts := make([]echotest.Option, 0, len(headers))
	for name, value := range headers {
		opts = append(opts, echotest.WithHeader(name, value))
	}
	c, rec := postWithWorkflow(t, "/v1/chat/completions", body, workflow, opts...)
	err := mw.HandleRequest(c, body, func() error { return next(c) })
	require.NoError(t, err)

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
	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, rec.Header().Get("X-Cache"))

	// Wait for the tracked background write to complete before the second request.
	mw.simple.wg.Wait()

	rec2 := driveHandleRequest(t, mw, workflow, body, nil, next)
	require.Equal(t, http.StatusOK, rec2.Code)
	require.Equal(t, "HIT (exact)", rec2.Header().Get("X-Cache"))
	require.Contains(t, rec2.Body.String(), "cached")
	require.Equal(t, 1, callCount)
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
	require.Empty(t, rec1.Header().Get("X-Cache"))

	mw.simple.wg.Wait()

	rec2 := driveHandleRequest(t, mw, workflow, body2, nil, next)
	require.Empty(t, rec2.Header().Get("X-Cache"))
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
	require.Equal(t, int32(1), calls.Load())

	for i := range requests {
		require.NoError(t, errs[i], "request %d", i)
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
	err := m.StoreAfter(follower, body, func() error { return nil })
	require.ErrorIs(t, err, context.Canceled)

	close(release)
	err = <-leaderDone
	require.NoError(t, err)
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
	err := <-leaderDone
	require.ErrorIs(t, err, leaderErr)

	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err, "follower %d", i)
	}
	require.Equal(t, int32(followers), followerCalls.Load())
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
	err := <-leaderDone
	require.NoError(t, err)
	err = <-followerDone
	require.NoError(t, err)

	m.wg.Wait()
	key := hashRequest("/v1/chat/completions", body, nil, "")
	cached, err := store.Get(context.Background(), key)
	require.NoError(t, err)
	require.NotEmpty(t, cached)
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
	require.NotNil(t, recovered)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var calls atomic.Int32
	err := m.StoreAfter(&blockingMissExchange{ctx: ctx}, body, func() error {
		calls.Add(1)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, int32(1), calls.Load())
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
			first := hashRequest("/v1/embeddings", []byte(tt.first), plan, "")
			second := hashRequest("/v1/embeddings", []byte(tt.second), plan, "")
			require.Equal(t, tt.equal, first == second, "%s / %s", first, second)
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
			first := hashRequest(tt.path, []byte(tt.duplicate), plan, "")
			second := hashRequest(tt.path, []byte(tt.collapsed), plan, "")
			require.NotEqual(t, second, first)
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
	}, "")
	second := hashRequest("/v1/chat/completions", body, &core.Workflow{
		Mode: core.ExecutionModeTranslated,
		Resolution: &core.RequestModelResolution{
			ResolvedSelector: core.ModelSelector{Provider: "anthropic", Model: "claude-opus-4-6"},
		},
	}, "")

	require.NotEqual(t, second, first)
}

func TestHashRequest_ModeChangesKey(t *testing.T) {
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)

	first := hashRequest("/v1/chat/completions", body, &core.Workflow{
		Mode: core.ExecutionModeTranslated,
	}, "")
	second := hashRequest("/v1/chat/completions", body, &core.Workflow{
		Mode: core.ExecutionModePassthrough,
	}, "")

	require.NotEqual(t, second, first)
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

	first := hashRequest("/v1/chat/completions", base, plan, "")
	second := hashRequest("/v1/chat/completions", withUsage, plan, "")

	require.NotEqual(t, second, first)
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

	first := hashRequest("/v1/chat/completions", base, plan, "")
	second := hashRequest("/v1/chat/completions", streaming, plan, "")

	require.NotEqual(t, second, first)
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
	require.Empty(t, rec1.Header().Get("X-Cache"))

	mw.simple.wg.Wait()

	rec2 := driveHandleRequest(t, mw, workflow, streamingBody, nil, makeNext(streamingBody))
	require.Empty(t, rec2.Header().Get("X-Cache"))
	require.Equal(t, "text/event-stream", rec2.Header().Get("Content-Type"))
	require.Equal(t, rawStream, rec2.Body.Bytes())
	require.Equal(t, 2, callCount)

	mw.simple.wg.Wait()

	rec3 := driveHandleRequest(t, mw, workflow, streamingBody, nil, makeNext(streamingBody))
	require.Equal(t, "HIT (exact)", rec3.Header().Get("X-Cache"))
	require.Equal(t, "text/event-stream", rec3.Header().Get("Content-Type"))
	require.Equal(t, rawStream, rec3.Body.Bytes())
	require.Equal(t, 2, callCount)

	rec4 := driveHandleRequest(t, mw, workflow, nonStreamingBody, nil, makeNext(nonStreamingBody))
	require.Equal(t, "HIT (exact)", rec4.Header().Get("X-Cache"))
	require.Equal(t, "application/json", rec4.Header().Get("Content-Type"))
	require.Contains(t, rec4.Body.String(), "json cached response")
	require.Equal(t, 2, callCount)
}

// driveChainedRequest drives HandleRequest with a guardrail chain identity on
// the request context, the way the inference orchestrator does for a request
// whose workflow declares guardrail steps.
func driveChainedRequest(
	t *testing.T,
	mw *ResponseCacheMiddleware,
	workflow *core.Workflow,
	chainHash string,
	body []byte,
	next func(c *echo.Context) error,
) *httptest.ResponseRecorder {
	t.Helper()
	c, rec := postWithWorkflow(t, "/v1/chat/completions", body, workflow)
	req := c.Request()
	c.SetRequest(req.WithContext(core.WithGuardrailsHash(req.Context(), chainHash)))
	err := mw.HandleRequest(c, body, func() error { return next(c) })
	require.NoError(t, err)

	return rec
}

func TestHashRequest_GuardrailChainChangesKey(t *testing.T) {
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	plan := resolvedWorkflow("openai", "gpt-4")

	none := hashRequest("/v1/chat/completions", body, plan, "")
	chainA := hashRequest("/v1/chat/completions", body, plan, "chain-a")
	chainB := hashRequest("/v1/chat/completions", body, plan, "chain-b")

	require.NotEqual(t, chainA, none)
	require.NotEqual(t, chainB, chainA)
	require.Equal(t, hashRequest("/v1/chat/completions", body, plan, "chain-a"), chainA)
}

// A body cached under one guardrail chain must never be replayed to a request
// whose response/stream chain differs: the replay skips the response phase, so
// the entry is only valid for the chain that produced it.
func TestHandleRequest_ExactCacheIsScopedToGuardrailChain(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()
	mw := NewResponseCacheMiddlewareWithStore(store, time.Hour)
	workflow := resolvedWorkflow("openai", "gpt-4")
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)

	calls := 0
	next := func(c *echo.Context) error {
		calls++
		return c.JSON(http.StatusOK, map[string]string{"result": "unguarded"})
	}
	require.Empty(t, driveChainedRequest(t, mw, workflow, "", body, next).Header().Get("X-Cache"))

	mw.simple.wg.Wait()
	// Same request, same (empty) chain: still a hit.
	require.Equal(t, "HIT (exact)", driveChainedRequest(t, mw, workflow, "", body, next).Header().Get("X-Cache"))
	require.Equal(t, 1, calls)

	// A workflow that adds a response-phase guardrail must miss instead of
	// receiving the body stored before the guardrail existed.
	guarded := driveChainedRequest(t, mw, workflow, "response-redaction-chain", body, func(c *echo.Context) error {
		calls++
		return c.JSON(http.StatusOK, map[string]string{"result": "guarded"})
	})
	require.Empty(t, guarded.Header().Get("X-Cache"))
	require.Contains(t, guarded.Body.String(), "guarded")
	require.Equal(t, 2, calls)

	mw.simple.wg.Wait()
	// The guarded entry is its own entry and replays only to its own chain.
	require.Equal(t, "HIT (exact)", driveChainedRequest(t, mw, workflow, "response-redaction-chain", body, next).Header().Get("X-Cache"))
	require.Equal(t, 2, calls)
}

// Two tenants sharing one cache but resolving different workflows must not
// share entries when their guardrail chains differ, with no config change.
func TestHandleRequest_ExactCacheIsolatesTenantsWithDifferentChains(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()
	mw := NewResponseCacheMiddlewareWithStore(store, time.Hour)
	workflow := resolvedWorkflow("openai", "gpt-4")
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"tenant-xtalk"}]}`)

	unguarded := driveChainedRequest(t, mw, workflow, "", body, func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"result": "ECHO"})
	})
	require.Empty(t, unguarded.Header().Get("X-Cache"))

	mw.simple.wg.Wait()

	rec := driveChainedRequest(t, mw, workflow, "tenant-b-chain", body, func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"result": "GUARDED"})
	})
	require.Empty(t, rec.Header().Get("X-Cache"))
	require.Contains(t, rec.Body.String(), "GUARDED")
}

// A streaming reply cached under one stream chain must not replay to a request
// whose stream guardrails differ; the SSE replay runs no stream phase.
func TestHandleRequest_StreamReplayIsScopedToGuardrailChain(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()
	mw := NewResponseCacheMiddlewareWithStore(store, time.Hour)
	workflow := resolvedWorkflow("openai", "gpt-4")
	body := []byte(`{"model":"gpt-4","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	rawStream := []byte(
		"data: {\"id\":\"chatcmpl-stream\",\"object\":\"chat.completion.chunk\",\"created\":1234567890,\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"sk-secret\"},\"finish_reason\":null}]}\n\n" +
			"data: {\"id\":\"chatcmpl-stream\",\"object\":\"chat.completion.chunk\",\"created\":1234567890,\"model\":\"gpt-4\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: [DONE]\n\n",
	)
	streamNext := func(c *echo.Context) error {
		c.Response().Header().Set("Content-Type", "text/event-stream")
		c.Response().WriteHeader(http.StatusOK)
		_, err := c.Response().Write(rawStream)
		return err
	}
	require.Empty(t, driveChainedRequest(t, mw, workflow, "", body, streamNext).Header().Get("X-Cache"))

	mw.simple.wg.Wait()
	require.Equal(t, "HIT (exact)", driveChainedRequest(t, mw, workflow, "", body, streamNext).Header().Get("X-Cache"))
	require.Empty(t, driveChainedRequest(t, mw, workflow, "stream-mask-chain", body, streamNext).Header().Get("X-Cache"))
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
			assert.Equal(t, tt.want, isStreamingRequest(tt.path, []byte(tt.body)))
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
		require.Empty(t, rec.Header().Get("X-Cache"))
	}
	require.Equal(t, 2, callCount)
}

func TestClose_WaitsForPendingWrites(t *testing.T) {
	store := cache.NewMapStore()
	mw := NewResponseCacheMiddlewareWithStore(store, time.Hour)
	workflow := resolvedWorkflow("openai", "gpt-4")

	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"close-test"}]}`)
	rec := driveHandleRequest(t, mw, workflow, body, nil, func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"result": "ok"})
	})
	require.Equal(t, http.StatusOK, rec.Code)
	// Close must drain any in-flight write before closing the store.
	// If Close races store.Close against the goroutine's Set, this will
	// panic or produce a data race under -race.
	err := mw.Close()
	require.NoError(t, err)
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
			c, rec := postWithWorkflow(t, "/v1/chat/completions", body, workflow)
			err := mw.HandleRequest(c, body, func() error {
				return c.JSON(http.StatusOK, map[string]string{"result": "ok"})
			})
			if assert.NoError(t, err) {
				assert.Equal(t, http.StatusOK, rec.Code)
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
	require.LessOrEqual(t, store.maxConcurrent.Load(), int32(cacheWriteWorkerCount))

	for range requestCount {
		store.releaseCh <- struct{}{}
	}
	reqWG.Wait()
	err := mw.Close()
	require.NoError(t, err)
}

func TestHandleRequest_BackgroundOnlyBypassesResponsesCreate(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		body      string
		wantCache string
		wantCalls int
	}{
		{
			name:      "responses create bypasses the cache",
			path:      "/v1/responses",
			body:      `{"model":"gpt-4","input":"hi","background":true}`,
			wantCache: "",
			wantCalls: 2,
		},
		{
			name:      "chat completions still caches",
			path:      "/v1/chat/completions",
			body:      `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"background":true}`,
			wantCache: "HIT (exact)",
			wantCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := cache.NewMapStore()
			defer store.Close()
			mw := NewResponseCacheMiddlewareWithStore(store, time.Hour)
			workflow := resolvedWorkflow("openai", "gpt-4")
			body := []byte(tt.body)
			callCount := 0
			drive := func() *httptest.ResponseRecorder {
				t.Helper()
				c, rec := postWithWorkflow(t, tt.path, body, workflow)
				err := mw.HandleRequest(c, body, func() error {
					callCount++
					return c.JSON(http.StatusOK, map[string]string{"id": "resp_1"})
				})
				require.NoError(t, err)

				return rec
			}
			rec := drive()
			require.Equal(t, http.StatusOK, rec.Code)

			mw.simple.wg.Wait()

			rec2 := drive()
			require.Equal(t, http.StatusOK, rec2.Code)
			require.Equal(t, tt.wantCache, rec2.Header().Get("X-Cache"))
			require.Equal(t, tt.wantCalls, callCount)
		})
	}
}
