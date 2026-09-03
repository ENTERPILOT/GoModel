package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/cache"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/responsecache"
)

// JSON parsers disagree on which duplicate object member wins (gjson keeps the
// first, encoding/json and provider parsers keep the last), so a body such as
// {"model":"allowed","model":"blocked"} could be authorized as one model and
// executed as another. These tests pin that every route rejects a repeated
// top-level model, provider, or stream field before it authorizes, caches,
// or forwards anything.

const duplicateSelectorErrPrefix = `duplicate top-level \"`

// newDuplicateSelectorPipeline wires the production ingress middleware chain
// (snapshot capture and workflow resolution) in front of the inference and
// passthrough handlers.
func newDuplicateSelectorPipeline(provider *mockProvider, authorizer RequestModelAuthorizer) (*echo.Echo, *Handler) {
	e := echo.New()
	e.Use(RequestSnapshotCapture())
	e.Use(WorkflowResolution(provider))
	handler := newHandlerWithAuthorizer(provider, nil, nil, nil, nil, authorizer, nil, nil, nil)
	e.POST("/v1/chat/completions", handler.ChatCompletion)
	e.POST("/v1/responses", handler.Responses)
	e.POST("/v1/embeddings", handler.Embeddings)
	e.POST("/v1/messages", handler.Messages)
	e.POST("/p/:provider/*", handler.ProviderPassthrough)
	return e, handler
}

func newDuplicateSelectorProvider() *mockProvider {
	return &mockProvider{
		supportedModels: []string{"allowed-model", "blocked-model", "text-embedding-3-small"},
		providerTypes: map[string]string{
			"openai/allowed-model": "openai",
			"openai/blocked-model": "openai",
		},
		// Any provider call would surface as a 502, so a 400 proves the
		// request never reached the provider.
		err:          errors.New("provider must not be called for a duplicate selector body"),
		embeddingErr: errors.New("provider must not be called for a duplicate selector body"),
		passthroughResponse: &core.PassthroughResponse{
			StatusCode: http.StatusOK,
			Headers:    http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		},
	}
}

func assertDuplicateSelectorRejected(t *testing.T, rec *httptest.ResponseRecorder, field string) {
	t.Helper()
	assert.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), duplicateSelectorErrPrefix+field+`\" field`)
	assert.NotEqual(t, "text/event-stream", rec.Header().Get("Content-Type"))
}

func TestDuplicateSelector_TranslatedRoutesRejectedAtIngress(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		body  string
		field string
	}{
		{
			name:  "chat duplicate model",
			path:  "/v1/chat/completions",
			body:  `{"model":"allowed-model","model":"blocked-model","messages":[{"role":"user","content":"hi"}]}`,
			field: "model",
		},
		{
			name:  "chat duplicate provider",
			path:  "/v1/chat/completions",
			body:  `{"provider":"openai","provider":"groq","model":"allowed-model","messages":[{"role":"user","content":"hi"}]}`,
			field: "provider",
		},
		{
			name:  "streaming chat duplicate stream",
			path:  "/v1/chat/completions",
			body:  `{"model":"allowed-model","stream":true,"stream":false,"messages":[{"role":"user","content":"hi"}]}`,
			field: "stream",
		},
		{
			name:  "streaming chat duplicate model",
			path:  "/v1/chat/completions",
			body:  `{"model":"allowed-model","model":"blocked-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			field: "model",
		},
		{
			name:  "responses duplicate model",
			path:  "/v1/responses",
			body:  `{"model":"allowed-model","model":"blocked-model","input":"hi"}`,
			field: "model",
		},
		{
			name:  "embeddings duplicate model",
			path:  "/v1/embeddings",
			body:  `{"model":"text-embedding-3-small","model":"blocked-model","input":"hi"}`,
			field: "model",
		},
		{
			name:  "anthropic messages duplicate model",
			path:  "/v1/messages",
			body:  `{"model":"allowed-model","model":"blocked-model","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
			field: "model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := newDuplicateSelectorProvider()
			authorizer := &recordingModelAuthorizer{}
			e, _ := newDuplicateSelectorPipeline(provider, authorizer)

			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			assertDuplicateSelectorRejected(t, rec, tt.field)
			assert.Equal(t, core.ModelSelector{}, authorizer.lastSelector, "no model must be authorized")
			assert.Equal(t, 0, provider.chatCompletionCalls)
		})
	}
}

func TestDuplicateSelector_TranslatedRouteRejectedWhenBodyIsNotCapturedAtIngress(t *testing.T) {
	// A duplicate that sits past the selector peek window is only visible once
	// the full body is read, which must still happen before any dispatch.
	padding := strings.Repeat("x", int(requestSelectorPeekLimit))
	tests := []struct {
		name          string
		body          string
		unknownLength bool
	}{
		{
			name:          "unknown length duplicate after first model",
			body:          `{"model":"allowed-model","messages":[{"role":"user","content":"hi"}],"model":"blocked-model"}`,
			unknownLength: true,
		},
		{
			name: "oversized body duplicate past the peek window",
			body: `{"model":"allowed-model","messages":[{"role":"user","content":"` + padding + `"}],"model":"blocked-model"}`,
		},
		{
			name: "oversized body with both selectors peeked then duplicated",
			body: `{"model":"allowed-model","provider":"openai","messages":[{"role":"user","content":"` + padding + `"}],"model":"blocked-model"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := newDuplicateSelectorProvider()
			authorizer := &recordingModelAuthorizer{}
			e, _ := newDuplicateSelectorPipeline(provider, authorizer)

			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			if tt.unknownLength {
				req.ContentLength = -1
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			assertDuplicateSelectorRejected(t, rec, "model")
			assert.Equal(t, 0, provider.chatCompletionCalls)
		})
	}
}

func TestDuplicateSelector_PassthroughRejectedBeforeForwarding(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		field         string
		unknownLength bool
	}{
		{
			name:  "captured body duplicate model",
			body:  `{"model":"allowed-model","model":"blocked-model","stream":true}`,
			field: "model",
		},
		{
			name:          "unknown length duplicate model",
			body:          `{"model":"allowed-model","model":"blocked-model","stream":true}`,
			field:         "model",
			unknownLength: true,
		},
		{
			name:  "streaming duplicate stream",
			body:  `{"model":"allowed-model","stream":true,"stream":false}`,
			field: "stream",
		},
		{
			name:  "duplicate provider",
			body:  `{"model":"allowed-model","provider":"openai","provider":"groq"}`,
			field: "provider",
		},
		{
			name:  "oversized body with duplicate past the peek window",
			body:  `{"model":"allowed-model","messages":[{"role":"user","content":"` + strings.Repeat("x", int(requestSelectorPeekLimit)) + `"}],"model":"blocked-model"}`,
			field: "model",
		},
		{
			name:          "oversized unknown length body with duplicate past the peek window",
			body:          `{"model":"allowed-model","messages":[{"role":"user","content":"` + strings.Repeat("x", int(requestSelectorPeekLimit)) + `"}],"model":"blocked-model"}`,
			field:         "model",
			unknownLength: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := newDuplicateSelectorProvider()
			authorizer := &recordingModelAuthorizer{}
			e, _ := newDuplicateSelectorPipeline(provider, authorizer)

			req := httptest.NewRequest(http.MethodPost, "/p/openai/chat/completions", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			if tt.unknownLength {
				req.ContentLength = -1
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			assertDuplicateSelectorRejected(t, rec, tt.field)
			assert.Equal(t, core.ModelSelector{}, authorizer.lastSelector, "no model must be authorized")
			assert.Nil(t, provider.lastPassthroughReq, "body must not be forwarded upstream")
		})
	}
}

func TestDuplicateSelector_PassthroughHandlerRejectsLateDetectedDuplicate(t *testing.T) {
	// When ingress only peeked and a later middleware read the whole body
	// (session detection does this), the handler must still refuse to forward.
	provider := newDuplicateSelectorProvider()
	authorizer := &recordingModelAuthorizer{}
	handler := newHandlerWithAuthorizer(provider, nil, nil, nil, nil, authorizer, nil, nil, nil)

	body := `{"model":"allowed-model","model":"blocked-model"}`
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/p/openai/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	snapshot := core.NewRequestSnapshot(http.MethodPost, "/p/openai/chat/completions", map[string]string{"provider": "openai", "endpoint": "chat/completions"}, nil, nil, "application/json", nil, false, "", nil)
	ctx := core.WithRequestSnapshot(req.Context(), snapshot)
	ctx = core.WithWhiteBoxPrompt(ctx, core.DeriveWhiteBoxPrompt(snapshot))
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Simulate the late full-body read.
	_, err := requestBodyBytes(c)
	require.ErrorContains(t, err, `duplicate top-level "model" field`)

	require.NoError(t, handler.ProviderPassthrough(c))
	assertDuplicateSelectorRejected(t, rec, "model")
	assert.Nil(t, provider.lastPassthroughReq, "body must not be forwarded upstream")
}

type countingCacheStore struct {
	cache.Store
	gets int
	sets int
}

func (s *countingCacheStore) Get(ctx context.Context, key string) ([]byte, error) {
	s.gets++
	return s.Store.Get(ctx, key)
}

func (s *countingCacheStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	s.sets++
	return s.Store.Set(ctx, key, value, ttl)
}

func TestDuplicateSelector_CachedRouteRejectedBeforeCacheLookup(t *testing.T) {
	store := &countingCacheStore{Store: cache.NewMapStore()}
	rcm := responsecache.NewResponseCacheMiddlewareWithStore(store, time.Hour)
	defer rcm.Close()

	provider := newDuplicateSelectorProvider()
	authorizer := &recordingModelAuthorizer{}
	e, handler := newDuplicateSelectorPipeline(provider, authorizer)
	handler.responseCache = rcm

	body := `{"model":"allowed-model","model":"blocked-model","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assertDuplicateSelectorRejected(t, rec, "model")
	assert.Equal(t, 0, store.gets, "cache must not be consulted")
	assert.Equal(t, 0, store.sets, "cache must not be written")
}

func TestDuplicateSelector_HandlerRejectsWithoutIngressMiddleware(t *testing.T) {
	// Embedders and unit tests call handlers directly; the body decode path
	// must enforce uniqueness on its own.
	provider := newDuplicateSelectorProvider()
	authorizer := &recordingModelAuthorizer{}
	handler := newHandlerWithAuthorizer(provider, nil, nil, nil, nil, authorizer, nil, nil, nil)

	e := echo.New()
	body := `{"model":"allowed-model","model":"blocked-model","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, handler.ChatCompletion(c))
	assertDuplicateSelectorRejected(t, rec, "model")
	assert.Equal(t, core.ModelSelector{}, authorizer.lastSelector)
	assert.Equal(t, 0, provider.chatCompletionCalls)
}

func TestDuplicateSelector_UniqueSelectorsStillServed(t *testing.T) {
	// Only top-level model, provider, and stream must be unique; repeated
	// members elsewhere keep Postel-style leniency.
	tests := []struct {
		name string
		path string
		body string
	}{
		{
			name: "chat",
			path: "/v1/chat/completions",
			body: `{"model":"allowed-model","messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "chat with nested and non-selector duplicates",
			path: "/v1/chat/completions",
			body: `{"model":"allowed-model","n":1,"n":1,"messages":[{"role":"user","content":"hi","model":"a","model":"b"}]}`,
		},
		{
			name: "passthrough",
			path: "/p/openai/chat/completions",
			body: `{"model":"allowed-model","stream":false}`,
		},
		{
			name: "oversized passthrough body authorizes the model past the peek window",
			path: "/p/openai/chat/completions",
			body: `{"messages":[{"role":"user","content":"` + strings.Repeat("x", int(requestSelectorPeekLimit)) + `"}],"model":"allowed-model"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := newDuplicateSelectorProvider()
			provider.err = nil
			provider.response = &core.ChatResponse{ID: "chatcmpl-1", Object: "chat.completion", Model: "allowed-model"}
			authorizer := &recordingModelAuthorizer{}
			e, _ := newDuplicateSelectorPipeline(provider, authorizer)

			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
			assert.Equal(t, "allowed-model", authorizer.lastSelector.Model)
		})
	}
}
