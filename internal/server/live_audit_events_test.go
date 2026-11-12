package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/cache"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/live"
	"github.com/enterpilot/gomodel/internal/responsecache"
)

// liveAuditGolden holds what a connected dashboard ends up with for each
// request shape. Regenerate with UPDATE_GOLDEN=1 only for an intended change
// of the live audit state.
const liveAuditGolden = "testdata/live_audit_states.golden.json"

// liveAuditTestLogger publishes like auditlog.Logger: audit.completed when
// the entry is written, audit.flushed once it is persisted.
type liveAuditTestLogger struct {
	broker *live.Broker
}

func (l liveAuditTestLogger) Write(entry *auditlog.LogEntry) {
	l.broker.PublishAuditEvent(auditlog.LiveEventAuditCompleted, entry)
	l.broker.PublishAuditEvent(auditlog.LiveEventAuditFlushed, entry)
}

func (l liveAuditTestLogger) Config() auditlog.Config {
	return auditlog.Config{Enabled: true, LogBodies: true, LogHeaders: true}
}

func (l liveAuditTestLogger) Close() error { return nil }

func (l liveAuditTestLogger) PublishLiveEvent(eventType string, entry *auditlog.LogEntry) {
	l.broker.PublishAuditEvent(eventType, entry)
}

// liveAuditRecorder collects the audit events a dashboard subscriber receives.
type liveAuditRecorder struct {
	sub    *live.Subscription
	events []live.Event
}

func (r *liveAuditRecorder) drain() {
	for {
		select {
		case event := <-r.sub.Events:
			if strings.HasPrefix(event.Type, "audit.") {
				r.events = append(r.events, event)
			}
		default:
			return
		}
	}
}

// state merges every event received so far the way the dashboard does: each
// event's members overlay the request's accumulated object.
func (r *liveAuditRecorder) state(t *testing.T) map[string]any {
	t.Helper()
	r.drain()
	merged := map[string]any{}
	for _, event := range r.events {
		var data map[string]any
		require.NoError(t, json.Unmarshal(event.Data, &data))
		mergeLiveState(merged, data)
	}
	normalizeLiveState(merged)
	return merged
}

func (r *liveAuditRecorder) count(eventType string) int {
	r.drain()
	n := 0
	for _, event := range r.events {
		if event.Type == eventType {
			n++
		}
	}
	return n
}

func mergeLiveState(dst, patch map[string]any) {
	for key, value := range patch {
		if nested, ok := value.(map[string]any); ok {
			if existing, ok := dst[key].(map[string]any); ok {
				mergeLiveState(existing, nested)
				continue
			}
		}
		dst[key] = value
	}
}

// normalizeLiveState drops the members that differ between runs.
func normalizeLiveState(value any) {
	switch v := value.(type) {
	case map[string]any:
		for _, key := range []string{"id", "timestamp", "duration_ns", "started_at", "created", "api_key_hash"} {
			delete(v, key)
		}
		for _, nested := range v {
			normalizeLiveState(nested)
		}
	case []any:
		for _, nested := range v {
			normalizeLiveState(nested)
		}
	}
}

// liveProbeProvider records the live state a dashboard shows while each
// upstream call is in flight.
type liveProbeProvider struct {
	*mockProvider
	probe func()
}

func (p liveProbeProvider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	p.probe()
	return p.mockProvider.ChatCompletion(ctx, req)
}

func (p liveProbeProvider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	p.probe()
	return p.mockProvider.StreamChatCompletion(ctx, req)
}

func (p liveProbeProvider) Passthrough(ctx context.Context, providerType string, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	p.probe()
	return p.mockProvider.Passthrough(ctx, providerType, req)
}

func (p liveProbeProvider) CreateBatchWithHints(ctx context.Context, providerType string, req *core.BatchRequest) (*core.BatchResponse, map[string]string, error) {
	p.probe()
	return p.mockProvider.CreateBatchWithHints(ctx, providerType, req)
}

type liveProbeFailoverProvider struct {
	*failoverProvider
	probe func()
}

func (p liveProbeFailoverProvider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	p.probe()
	return p.failoverProvider.ChatCompletion(ctx, req)
}

type liveAuditState struct {
	InFlight []map[string]any `json:"in_flight"`
	Final    map[string]any   `json:"final"`
}

type liveAuditRequest struct {
	method string
	path   string
	body   string
}

const liveChatBody = `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"Hi"}]}`

func liveChatMock() *mockProvider {
	return &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		providerTypes:   map[string]string{"gpt-4o-mini": "openai"},
		providerNames:   map[string]string{"gpt-4o-mini": "openai"},
		response: &core.ChatResponse{
			ID: "chatcmpl-live", Object: "chat.completion", Model: "gpt-4o-mini",
			Choices: []core.Choice{{Index: 0, Message: core.ResponseMessage{Role: "assistant", Content: "Hello"}, FinishReason: "stop"}},
			Usage:   core.Usage{PromptTokens: 3, CompletionTokens: 1, TotalTokens: 4},
		},
		streamData: "data: {\"id\":\"chatcmpl-live\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hel\"}}]}\n\n" +
			"data: {\"id\":\"chatcmpl-live\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: [DONE]\n\n",
	}
}

func liveFailoverMock() *failoverProvider {
	return &failoverProvider{
		chatResponses: map[string]*core.ChatResponse{
			"azure/gpt-4o": {
				ID: "chatcmpl-failover", Object: "chat.completion", Model: "gpt-4o", Provider: "azure",
				Choices: []core.Choice{{Index: 0, Message: core.ResponseMessage{Role: "assistant", Content: "failover ok"}, FinishReason: "stop"}},
			},
		},
		chatErrors: map[string]error{
			"gpt-4o": core.NewProviderError("openai", http.StatusServiceUnavailable, "model temporarily unavailable", nil),
		},
		supportedModels: map[string]string{"gpt-4o": "openai", "azure/gpt-4o": "azure"},
	}
}

// TestLiveAuditEvents_RequestShapes pins the live audit state a connected
// dashboard sees for each request shape — while every upstream call is in
// flight and once the request finished — and bounds how many audit.updated
// events a request costs.
func TestLiveAuditEvents_RequestShapes(t *testing.T) {
	passthroughJSON := func() *core.PassthroughResponse {
		return &core.PassthroughResponse{
			StatusCode: http.StatusOK,
			Headers:    map[string][]string{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		}
	}
	passthroughSSE := func() *core.PassthroughResponse {
		return &core.PassthroughResponse{
			StatusCode: http.StatusOK,
			Headers:    map[string][]string{"Content-Type": {"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\"}}\n\ndata: [DONE]\n\n")),
		}
	}

	tests := []struct {
		name         string
		setup        func(probe func()) (core.RoutableProvider, *Config)
		warmup       *liveAuditRequest
		request      liveAuditRequest
		wantStatus   int
		maxUpdated   int
		wantInFlight int
	}{
		{
			name: "chat",
			setup: func(probe func()) (core.RoutableProvider, *Config) {
				return liveProbeProvider{liveChatMock(), probe}, &Config{}
			},
			request:      liveAuditRequest{http.MethodPost, "/v1/chat/completions", liveChatBody},
			wantStatus:   http.StatusOK,
			maxUpdated:   1,
			wantInFlight: 1,
		},
		{
			name: "chat stream",
			setup: func(probe func()) (core.RoutableProvider, *Config) {
				return liveProbeProvider{liveChatMock(), probe}, &Config{}
			},
			request:      liveAuditRequest{http.MethodPost, "/v1/chat/completions", `{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"Hi"}]}`},
			wantStatus:   http.StatusOK,
			maxUpdated:   2,
			wantInFlight: 1,
		},
		{
			name: "error before upstream",
			setup: func(probe func()) (core.RoutableProvider, *Config) {
				return liveProbeProvider{liveChatMock(), probe}, &Config{}
			},
			request:    liveAuditRequest{http.MethodPost, "/v1/chat/completions", `{"model":"unknown-model","messages":[{"role":"user","content":"Hi"}]}`},
			wantStatus: http.StatusNotFound,
			maxUpdated: 1,
		},
		{
			name: "upstream error",
			setup: func(probe func()) (core.RoutableProvider, *Config) {
				mock := liveChatMock()
				mock.err = core.NewProviderError("openai", http.StatusInternalServerError, "upstream exploded", nil)
				return liveProbeProvider{mock, probe}, &Config{}
			},
			request:      liveAuditRequest{http.MethodPost, "/v1/chat/completions", liveChatBody},
			wantStatus:   http.StatusInternalServerError,
			maxUpdated:   2,
			wantInFlight: 1,
		},
		{
			name: "failover",
			setup: func(probe func()) (core.RoutableProvider, *Config) {
				return liveProbeFailoverProvider{liveFailoverMock(), probe}, &Config{
					FailoverResolver: failoverResolverStub{selectors: []core.ModelSelector{{Provider: "azure", Model: "gpt-4o"}}},
				}
			},
			request:      liveAuditRequest{http.MethodPost, "/v1/chat/completions", `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`},
			wantStatus:   http.StatusOK,
			maxUpdated:   2,
			wantInFlight: 2,
		},
		{
			name: "exact cache hit",
			setup: func(probe func()) (core.RoutableProvider, *Config) {
				store := cache.NewMapStore()
				t.Cleanup(func() { _ = store.Close() })
				return liveProbeProvider{liveChatMock(), probe}, &Config{
					ResponseCacheMiddleware: responsecache.NewResponseCacheMiddlewareWithStore(store, time.Hour),
				}
			},
			warmup:     &liveAuditRequest{http.MethodPost, "/v1/chat/completions", liveChatBody},
			request:    liveAuditRequest{http.MethodPost, "/v1/chat/completions", liveChatBody},
			wantStatus: http.StatusOK,
			maxUpdated: 1,
		},
		{
			name: "exact cache hit stream",
			setup: func(probe func()) (core.RoutableProvider, *Config) {
				store := cache.NewMapStore()
				t.Cleanup(func() { _ = store.Close() })
				return liveProbeProvider{liveChatMock(), probe}, &Config{
					ResponseCacheMiddleware: responsecache.NewResponseCacheMiddlewareWithStore(store, time.Hour),
				}
			},
			warmup:     &liveAuditRequest{http.MethodPost, "/v1/chat/completions", `{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"Hi"}]}`},
			request:    liveAuditRequest{http.MethodPost, "/v1/chat/completions", `{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"Hi"}]}`},
			wantStatus: http.StatusOK,
			maxUpdated: 1,
		},
		{
			name: "passthrough",
			setup: func(probe func()) (core.RoutableProvider, *Config) {
				mock := liveChatMock()
				mock.passthroughResponse = passthroughJSON()
				return liveProbeProvider{mock, probe}, &Config{}
			},
			request:      liveAuditRequest{http.MethodPost, "/p/openai/responses", `{"model":"gpt-5-mini"}`},
			wantStatus:   http.StatusOK,
			maxUpdated:   1,
			wantInFlight: 1,
		},
		{
			name: "passthrough stream",
			setup: func(probe func()) (core.RoutableProvider, *Config) {
				mock := liveChatMock()
				mock.passthroughResponse = passthroughSSE()
				return liveProbeProvider{mock, probe}, &Config{}
			},
			request:      liveAuditRequest{http.MethodPost, "/p/openai/responses", `{"model":"gpt-5-mini","stream":true}`},
			wantStatus:   http.StatusOK,
			maxUpdated:   2,
			wantInFlight: 1,
		},
		{
			name: "batch",
			setup: func(probe func()) (core.RoutableProvider, *Config) {
				return liveProbeProvider{liveChatMock(), probe}, &Config{}
			},
			request: liveAuditRequest{http.MethodPost, "/v1/batches", `{"completion_window":"24h","requests":[{"custom_id":"chat-1","method":"POST","url":"/v1/chat/completions",` +
				`"body":{"model":"gpt-4o-mini","messages":[{"role":"user","content":"Hi"}]}}]}`},
			wantStatus:   http.StatusOK,
			maxUpdated:   1,
			wantInFlight: 1,
		},
	}

	got := map[string]liveAuditState{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			broker := live.NewBroker(live.Config{Enabled: true})
			t.Cleanup(broker.Close)
			recorder := &liveAuditRecorder{}
			var inFlight []map[string]any
			probing := false
			upstreamCalls := 0
			provider, cfg := tt.setup(func() {
				upstreamCalls++
				if probing {
					inFlight = append(inFlight, recorder.state(t))
				}
			})
			cfg.MasterKey = "live-master-key"
			cfg.AuditLogger = liveAuditTestLogger{broker: broker}
			srv := New(provider, cfg)

			send := func(r liveAuditRequest, requestID string) {
				req := httptest.NewRequest(r.method, r.path, strings.NewReader(r.body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer live-master-key")
				req.Header.Set(core.RequestIDHeader, requestID)
				rec := httptest.NewRecorder()
				srv.ServeHTTP(rec, req)
				require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			}
			if tt.warmup != nil {
				// The cache stores off the request path; wait until the
				// warmed-up answer is served without an upstream call.
				send(*tt.warmup, "live-warmup")
				require.Eventually(t, func() bool {
					before := upstreamCalls
					send(*tt.warmup, "live-warmup")
					return upstreamCalls == before
				}, 5*time.Second, 10*time.Millisecond)
			}

			recorder.sub = broker.Subscribe(broker.LatestSeq())
			t.Cleanup(recorder.sub.Close)
			probing = true
			send(tt.request, "live-request")

			assert.Len(t, inFlight, tt.wantInFlight)
			assert.LessOrEqual(t, recorder.count(auditlog.LiveEventAuditUpdated), tt.maxUpdated)
			assert.Equal(t, 1, recorder.count(auditlog.LiveEventAuditStarted))
			assert.Equal(t, 1, recorder.count(auditlog.LiveEventAuditFlushed))
			got[tt.name] = liveAuditState{InFlight: inFlight, Final: recorder.state(t)}
		})
	}

	encoded, err := json.MarshalIndent(got, "", "  ")
	require.NoError(t, err)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		require.NoError(t, os.MkdirAll(filepath.Dir(liveAuditGolden), 0o755))
		require.NoError(t, os.WriteFile(liveAuditGolden, append(encoded, '\n'), 0o644))
		return
	}
	want, err := os.ReadFile(liveAuditGolden)
	require.NoError(t, err, "run UPDATE_GOLDEN=1 go test ./internal/server -run TestLiveAuditEvents_RequestShapes")
	assert.JSONEq(t, string(want), string(encoded))
}
