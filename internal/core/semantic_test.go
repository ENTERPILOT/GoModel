package core

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

var benchmarkSemanticSelectorBody = []byte(`{
	"model":"gpt-5-mini",
	"provider":"openai",
	"stream":true,
	"messages":[{"role":"user","content":"hello"}],
	"response_format":{"type":"json_schema"}
}`)

func TestDeriveWhiteBoxPrompt_OpenAICompat(t *testing.T) {
	frame := NewRequestSnapshot(
		"POST",
		"/v1/chat/completions",
		nil,
		nil,
		nil,
		"application/json",
		[]byte(`{
			"model":"gpt-5-mini",
			"provider":"openai",
			"stream":true,
			"messages":[{"role":"user","content":"hello"}],
			"response_format":{"type":"json_schema"}
		}`),
		false,
		"",
		nil,
	)

	env := DeriveWhiteBoxPrompt(frame)
	require.NotNil(t, env)
	require.Equal(t, "openai_compat", env.RouteType)
	require.Equal(t, "chat_completions", env.OperationType)
	require.True(t, env.JSONBodyParsed)
	require.Equal(t, "gpt-5-mini", env.RouteHints.Model)
	require.Equal(t, "openai", env.RouteHints.Provider)
	require.True(t, env.StreamRequested)
	require.Nil(t, env.CachedChatRequest())
	require.Nil(t, env.CachedResponsesRequest())
	require.Nil(t, env.CachedEmbeddingRequest())
	require.Nil(t, env.CachedBatchRequest())
	require.Nil(t, env.CachedBatchRouteInfo())
	require.Nil(t, env.CachedFileRouteInfo())
	require.Nil(t, env.CachedPassthroughRouteInfo(), "canonical request payloads should be nil, got %+v", env)
}

func TestDeriveWhiteBoxPrompt_InvalidJSONRemainsPartial(t *testing.T) {
	frame := NewRequestSnapshot("POST", "/v1/responses", nil, nil, nil, "application/json", []byte(`{invalid}`), false, "", nil)

	env := DeriveWhiteBoxPrompt(frame)
	require.NotNil(t, env)
	require.Equal(t, "openai_compat", env.RouteType)
	require.Equal(t, "responses", env.OperationType)
	require.False(t, env.JSONBodyParsed)
	require.Empty(t, env.RouteHints.Model)
}

func TestDeriveWhiteBoxPrompt_PassthroughRouteParams(t *testing.T) {
	frame := NewRequestSnapshot(
		"POST",
		"/p/openai/responses",
		map[string]string{"provider": "openai", "endpoint": "responses"},
		nil,
		nil,
		"",
		[]byte(`{"model":"gpt-5-mini","stream":true,"foo":"bar"}`),
		false,
		"",
		nil,
	)

	env := DeriveWhiteBoxPrompt(frame)
	require.NotNil(t, env)
	require.Equal(t, "provider_passthrough", env.RouteType)
	require.Equal(t, "provider_passthrough", env.OperationType)
	require.Equal(t, "openai", env.RouteHints.Provider)
	require.Equal(t, "responses", env.RouteHints.Endpoint)
	require.Equal(t, "gpt-5-mini", env.RouteHints.Model)
	require.True(t, env.StreamRequested)

	info := env.CachedPassthroughRouteInfo()
	require.NotNil(t, info)
	require.Equal(t, "openai", info.Provider)
	require.Equal(t, "responses", info.RawEndpoint)
	require.Equal(t, "gpt-5-mini", info.Model)
	require.True(t, info.Stream)
	require.Equal(t, "/p/openai/responses", info.AuditPath)
	require.Nil(t, env.CachedChatRequest())
	require.Nil(t, env.CachedResponsesRequest())
	require.Nil(t, env.CachedEmbeddingRequest())
	require.Nil(t, env.CachedBatchRequest())
	require.Nil(t, env.CachedBatchRouteInfo())
	require.Nil(t, env.CachedFileRouteInfo(), "canonical request payloads should be nil, got %+v", env)
}

func TestDeriveWhiteBoxPrompt_PassthroughPathFallback(t *testing.T) {
	frame := NewRequestSnapshot("POST", "/p/anthropic/messages", nil, nil, nil, "", []byte(`{"model":"claude-sonnet-4-5"}`), false, "", nil)

	env := DeriveWhiteBoxPrompt(frame)
	require.NotNil(t, env)
	require.Equal(t, "anthropic", env.RouteHints.Provider)
	require.Equal(t, "messages", env.RouteHints.Endpoint)

	info := env.CachedPassthroughRouteInfo()
	require.NotNil(t, info)
	require.Equal(t, "anthropic", info.Provider)
	require.Equal(t, "messages", info.RawEndpoint)
}

func TestDeriveWhiteBoxPrompt_SkipsBodyParsingWhenIngressBodyWasNotCaptured(t *testing.T) {
	frame := NewRequestSnapshot("POST", "/v1/chat/completions", nil, nil, nil, "", nil, true, "", nil)

	env := DeriveWhiteBoxPrompt(frame)
	require.NotNil(t, env)
	require.False(t, env.JSONBodyParsed)
	require.Empty(t, env.RouteHints.Model)
}

func TestApplyBodyStreamHintPreservesSelectorConfidence(t *testing.T) {
	ApplyBodyStreamHint(nil, true)

	env := &WhiteBoxPrompt{RouteHints: RouteHints{Model: "existing-model", Provider: "openai"}}
	CachePassthroughRouteInfo(env, &PassthroughRouteInfo{
		Provider:        "openai",
		Model:           "existing-model",
		StreamUncertain: true,
	})

	ApplyBodyStreamHint(env, true)

	require.False(t, env.JSONBodyParsed)
	require.True(t, env.StreamRequested)
	require.Equal(t, "existing-model", env.RouteHints.Model)
	require.Equal(t, "openai", env.RouteHints.Provider, "RouteHints = %+v, want existing selector preserved", env.RouteHints)

	info := env.CachedPassthroughRouteInfo()
	require.NotNil(t, info)
	require.Equal(t, "existing-model", info.Model)
	require.True(t, info.Stream)
	require.False(t, info.StreamUncertain, "PassthroughRouteInfo = %+v, want model preserved and stream confirmed", info)
}

func TestApplyPartialBodyStreamHintPreservesUncertainty(t *testing.T) {
	ApplyPartialBodyStreamHint(nil, true)

	env := &WhiteBoxPrompt{RouteHints: RouteHints{Model: "existing-model", Provider: "openai"}}
	CachePassthroughRouteInfo(env, &PassthroughRouteInfo{
		Provider: "openai",
		Model:    "existing-model",
	})

	ApplyPartialBodyStreamHint(env, true)

	require.False(t, env.JSONBodyParsed)
	require.True(t, env.StreamRequested)
	require.Equal(t, "existing-model", env.RouteHints.Model)
	require.Equal(t, "openai", env.RouteHints.Provider, "RouteHints = %+v, want existing selector preserved", env.RouteHints)

	info := env.CachedPassthroughRouteInfo()
	require.NotNil(t, info)
	require.Equal(t, "existing-model", info.Model)
	require.True(t, info.Stream)
	require.True(t, info.StreamUncertain, "PassthroughRouteInfo = %+v, want model preserved and stream uncertain", info)
}

func TestDeriveWhiteBoxPrompt_FilesMetadata(t *testing.T) {
	frame := NewRequestSnapshot(
		"GET",
		"/v1/files/file_123/content",
		map[string]string{"id": "file_123"},
		map[string][]string{
			"provider": {"openai"},
		},
		nil,
		"application/octet-stream",
		nil,
		false,
		"",
		nil,
	)

	env := DeriveWhiteBoxPrompt(frame)
	require.NotNil(t, env)
	require.Equal(t, "files", env.OperationType)

	req := env.CachedFileRouteInfo()
	require.NotNil(t, req)
	require.Equal(t, FileActionContent, req.Action)
	require.Equal(t, "file_123", req.FileID)
	require.Equal(t, "openai", req.Provider)
	require.Equal(t, "openai", env.RouteHints.Provider)
}

func TestDeriveWhiteBoxPrompt_BatchesListMetadata(t *testing.T) {
	frame := NewRequestSnapshot(
		http.MethodGet,
		"/v1/batches",
		nil,
		map[string][]string{
			"after": {"batch_prev"},
			"limit": {"5"},
		},
		nil,
		"",
		nil,
		false,
		"",
		nil,
	)

	env := DeriveWhiteBoxPrompt(frame)
	require.NotNil(t, env)
	require.Equal(t, "batches", env.OperationType)

	req := env.CachedBatchRouteInfo()
	require.NotNil(t, req)
	require.Equal(t, BatchActionList, req.Action)
	require.Equal(t, "batch_prev", req.After)
	require.True(t, req.HasLimit)
	require.Equal(t, 5, req.Limit)
}

func TestDeriveWhiteBoxPrompt_BatchResultsMetadata(t *testing.T) {
	frame := NewRequestSnapshot(http.MethodGet, "/v1/batches/batch_123/results", map[string]string{"id": "batch_123"}, nil, nil, "", nil, false, "", nil)

	env := DeriveWhiteBoxPrompt(frame)
	require.NotNil(t, env)
	require.Equal(t, "batches", env.OperationType)

	req := env.CachedBatchRouteInfo()
	require.NotNil(t, req)
	require.Equal(t, BatchActionResults, req.Action)
	require.Equal(t, "batch_123", req.BatchID)
}

func TestDeriveSnapshotSelectorHintsGJSON_MatchesStdlibSemantics(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		wantModel    string
		wantProvider string
		wantStream   bool
		wantParsed   bool
	}{
		{name: "valid selector fields", body: `{"provider":"openai","model":"gpt-5-mini","stream":true}`, wantModel: "gpt-5-mini", wantProvider: "openai", wantStream: true, wantParsed: true},
		{name: "duplicate selector fields use first occurrence", body: `{"model":"blocked","model":"gpt-5-mini","provider":"x","provider":"openai","stream":false,"stream":true}`, wantModel: "blocked", wantProvider: "x", wantStream: false, wantParsed: true},
		{name: "duplicate null string keeps first value and null stream keeps first bool", body: `{"model":"gpt-5-mini","model":null,"provider":"openai","provider":null,"stream":true,"stream":null}`, wantModel: "gpt-5-mini", wantProvider: "openai", wantStream: true, wantParsed: true},
		{name: "duplicate invalid selector field keeps first value", body: `{"model":"gpt-5-mini","model":123}`, wantModel: "gpt-5-mini", wantParsed: true},
		{name: "duplicate invalid stream field keeps first value", body: `{"stream":true,"stream":"yes"}`, wantStream: true, wantParsed: true},
		{name: "missing selector fields", body: `{"messages":[{"role":"user","content":"hi"}]}`, wantParsed: true},
		{name: "null selector fields", body: `{"provider":null,"model":null,"stream":null}`, wantParsed: true},
		{name: "invalid json", body: `not json`, wantParsed: false},
		{name: "array root", body: `[]`, wantParsed: false},
		{name: "numeric model", body: `{"model":123}`, wantParsed: false},
		{name: "numeric provider", body: `{"provider":123}`, wantParsed: false},
		{name: "string stream", body: `{"stream":"true"}`, wantParsed: false},
		{name: "mixed valid and invalid", body: `{"model":"gpt-5-mini","provider":"openai","stream":"true"}`, wantParsed: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotModel, gotProvider, gotStream, gotParsed := deriveSnapshotSelectorHintsGJSON([]byte(tt.body))
			require.Equal(t, tt.wantModel, gotModel)
			require.Equal(t, tt.wantProvider, gotProvider)
			require.Equal(t, tt.wantStream, gotStream)
			require.Equal(t, tt.wantParsed, gotParsed)
		})
	}
}

func BenchmarkDeriveSnapshotSelectorHintsGJSON(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		model, provider, stream, parsed := deriveSnapshotSelectorHintsGJSON(benchmarkSemanticSelectorBody)
		if !parsed || model != "gpt-5-mini" || provider != "openai" || !stream {
			b.Fatalf("unexpected selector hints: parsed=%v model=%q provider=%q stream=%v", parsed, model, provider, stream)
		}
	}
}

func TestDeriveBatchRouteInfoFromTransport_MessagesBatches(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		wantAction string
		wantID     string
	}{
		{name: "create", method: http.MethodPost, path: "/v1/messages/batches", wantAction: BatchActionCreate},
		{name: "list", method: http.MethodGet, path: "/v1/messages/batches", wantAction: BatchActionList},
		{name: "get", method: http.MethodGet, path: "/v1/messages/batches/msgbatch_1", wantAction: BatchActionGet, wantID: "msgbatch_1"},
		{name: "cancel", method: http.MethodPost, path: "/v1/messages/batches/msgbatch_1/cancel", wantAction: BatchActionCancel, wantID: "msgbatch_1"},
		{name: "delete", method: http.MethodDelete, path: "/v1/messages/batches/msgbatch_1", wantAction: BatchActionDelete, wantID: "msgbatch_1"},
		{name: "results", method: http.MethodGet, path: "/v1/messages/batches/msgbatch_1/results", wantAction: BatchActionResults, wantID: "msgbatch_1"},
		{name: "openai delete", method: http.MethodDelete, path: "/v1/batches/batch_1", wantAction: BatchActionDelete, wantID: "batch_1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DeriveBatchRouteInfoFromTransport(tc.method, tc.path, nil, nil)
			require.NotNil(t, got)
			require.Equal(t, tc.wantAction, got.Action)
			require.Equal(t, tc.wantID, got.BatchID)
		})
	}
}
