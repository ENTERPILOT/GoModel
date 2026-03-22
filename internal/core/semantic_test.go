package core

import (
	"net/http"
	"testing"
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
	if env == nil {
		t.Fatal("DeriveWhiteBoxPrompt() = nil")
		return
	}
	if env.RouteType != "openai_compat" {
		t.Fatalf("RouteType = %q, want openai_compat", env.RouteType)
	}
	if env.OperationType != "chat_completions" {
		t.Fatalf("OperationType = %q, want chat_completions", env.OperationType)
	}
	if !env.JSONBodyParsed {
		t.Fatal("JSONBodyParsed = false, want true")
	}
	if env.RouteHints.Model != "gpt-5-mini" {
		t.Fatalf("RouteHints.Model = %q, want gpt-5-mini", env.RouteHints.Model)
	}
	if env.RouteHints.Provider != "openai" {
		t.Fatalf("RouteHints.Provider = %q, want openai", env.RouteHints.Provider)
	}
	if !env.StreamRequested {
		t.Fatal("StreamRequested = false, want true")
	}
	if env.CachedChatRequest() != nil || env.CachedResponsesRequest() != nil || env.CachedEmbeddingRequest() != nil || env.CachedBatchRequest() != nil || env.CachedBatchRouteInfo() != nil || env.CachedFileRouteInfo() != nil || env.CachedPassthroughRouteInfo() != nil {
		t.Fatalf("canonical request payloads should be nil, got %+v", env)
	}
}

func TestDeriveWhiteBoxPrompt_InvalidJSONRemainsPartial(t *testing.T) {
	frame := NewRequestSnapshot("POST", "/v1/responses", nil, nil, nil, "application/json", []byte(`{invalid}`), false, "", nil)

	env := DeriveWhiteBoxPrompt(frame)
	if env == nil {
		t.Fatal("DeriveWhiteBoxPrompt() = nil")
		return
	}
	if env.RouteType != "openai_compat" {
		t.Fatalf("RouteType = %q, want openai_compat", env.RouteType)
	}
	if env.OperationType != "responses" {
		t.Fatalf("OperationType = %q, want responses", env.OperationType)
	}
	if env.JSONBodyParsed {
		t.Fatal("JSONBodyParsed = true, want false")
	}
	if env.RouteHints.Model != "" {
		t.Fatalf("RouteHints.Model = %q, want empty", env.RouteHints.Model)
	}
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
	if env == nil {
		t.Fatal("DeriveWhiteBoxPrompt() = nil")
		return
	}
	if env.RouteType != "provider_passthrough" {
		t.Fatalf("RouteType = %q, want provider_passthrough", env.RouteType)
	}
	if env.OperationType != "provider_passthrough" {
		t.Fatalf("OperationType = %q, want provider_passthrough", env.OperationType)
	}
	if env.RouteHints.Provider != "openai" {
		t.Fatalf("RouteHints.Provider = %q, want openai", env.RouteHints.Provider)
	}
	if env.RouteHints.Endpoint != "responses" {
		t.Fatalf("RouteHints.Endpoint = %q, want responses", env.RouteHints.Endpoint)
	}
	if env.RouteHints.Model != "gpt-5-mini" {
		t.Fatalf("RouteHints.Model = %q, want gpt-5-mini", env.RouteHints.Model)
	}
	if !env.StreamRequested {
		t.Fatal("StreamRequested = false, want true")
	}
	info := env.CachedPassthroughRouteInfo()
	if info == nil {
		t.Fatal("CachedPassthroughRouteInfo() = nil")
	}
	if info.Provider != "openai" {
		t.Fatalf("PassthroughRouteInfo.Provider = %q, want openai", info.Provider)
	}
	if info.RawEndpoint != "responses" {
		t.Fatalf("PassthroughRouteInfo.RawEndpoint = %q, want responses", info.RawEndpoint)
	}
	if info.Model != "gpt-5-mini" {
		t.Fatalf("PassthroughRouteInfo.Model = %q, want gpt-5-mini", info.Model)
	}
	if info.AuditPath != "/p/openai/responses" {
		t.Fatalf("PassthroughRouteInfo.AuditPath = %q, want /p/openai/responses", info.AuditPath)
	}
	if env.CachedChatRequest() != nil || env.CachedResponsesRequest() != nil || env.CachedEmbeddingRequest() != nil || env.CachedBatchRequest() != nil || env.CachedBatchRouteInfo() != nil || env.CachedFileRouteInfo() != nil {
		t.Fatalf("canonical request payloads should be nil, got %+v", env)
	}
}

func TestDeriveWhiteBoxPrompt_PassthroughPathFallback(t *testing.T) {
	frame := NewRequestSnapshot("POST", "/p/anthropic/messages", nil, nil, nil, "", []byte(`{"model":"claude-sonnet-4-5"}`), false, "", nil)

	env := DeriveWhiteBoxPrompt(frame)
	if env == nil {
		t.Fatal("DeriveWhiteBoxPrompt() = nil")
		return
	}
	if env.RouteHints.Provider != "anthropic" {
		t.Fatalf("RouteHints.Provider = %q, want anthropic", env.RouteHints.Provider)
	}
	if env.RouteHints.Endpoint != "messages" {
		t.Fatalf("RouteHints.Endpoint = %q, want messages", env.RouteHints.Endpoint)
	}
	info := env.CachedPassthroughRouteInfo()
	if info == nil {
		t.Fatal("CachedPassthroughRouteInfo() = nil")
	}
	if info.Provider != "anthropic" {
		t.Fatalf("PassthroughRouteInfo.Provider = %q, want anthropic", info.Provider)
	}
	if info.RawEndpoint != "messages" {
		t.Fatalf("PassthroughRouteInfo.RawEndpoint = %q, want messages", info.RawEndpoint)
	}
}

func TestDeriveWhiteBoxPrompt_SkipsBodyParsingWhenIngressBodyWasNotCaptured(t *testing.T) {
	frame := NewRequestSnapshot("POST", "/v1/chat/completions", nil, nil, nil, "", nil, true, "", nil)

	env := DeriveWhiteBoxPrompt(frame)
	if env == nil {
		t.Fatal("DeriveWhiteBoxPrompt() = nil")
		return
	}
	if env.JSONBodyParsed {
		t.Fatal("JSONBodyParsed = true, want false")
	}
	if env.RouteHints.Model != "" {
		t.Fatalf("RouteHints.Model = %q, want empty", env.RouteHints.Model)
	}
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
	if env == nil {
		t.Fatal("DeriveWhiteBoxPrompt() = nil")
		return
	}
	if env.OperationType != "files" {
		t.Fatalf("OperationType = %q, want files", env.OperationType)
	}
	req := env.CachedFileRouteInfo()
	if req == nil {
		t.Fatal("FileRequest = nil")
		return
	}
	if req.Action != FileActionContent {
		t.Fatalf("FileRequest.Action = %q, want %q", req.Action, FileActionContent)
	}
	if req.FileID != "file_123" {
		t.Fatalf("FileRequest.FileID = %q, want file_123", req.FileID)
	}
	if req.Provider != "openai" {
		t.Fatalf("FileRequest.Provider = %q, want openai", req.Provider)
	}
	if env.RouteHints.Provider != "openai" {
		t.Fatalf("RouteHints.Provider = %q, want openai", env.RouteHints.Provider)
	}
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
	if env == nil {
		t.Fatal("DeriveWhiteBoxPrompt() = nil")
		return
	}
	if env.OperationType != "batches" {
		t.Fatalf("OperationType = %q, want batches", env.OperationType)
	}
	req := env.CachedBatchRouteInfo()
	if req == nil {
		t.Fatal("BatchMetadata = nil")
		return
	}
	if req.Action != BatchActionList {
		t.Fatalf("BatchMetadata.Action = %q, want %q", req.Action, BatchActionList)
	}
	if req.After != "batch_prev" {
		t.Fatalf("BatchMetadata.After = %q, want batch_prev", req.After)
	}
	if !req.HasLimit || req.Limit != 5 {
		t.Fatalf("BatchMetadata limit = %d/%v, want 5/true", req.Limit, req.HasLimit)
	}
}

func TestDeriveWhiteBoxPrompt_BatchResultsMetadata(t *testing.T) {
	frame := NewRequestSnapshot(http.MethodGet, "/v1/batches/batch_123/results", map[string]string{"id": "batch_123"}, nil, nil, "", nil, false, "", nil)

	env := DeriveWhiteBoxPrompt(frame)
	if env == nil {
		t.Fatal("DeriveWhiteBoxPrompt() = nil")
		return
	}
	if env.OperationType != "batches" {
		t.Fatalf("OperationType = %q, want batches", env.OperationType)
	}
	req := env.CachedBatchRouteInfo()
	if req == nil {
		t.Fatal("BatchMetadata = nil")
		return
	}
	if req.Action != BatchActionResults {
		t.Fatalf("BatchMetadata.Action = %q, want %q", req.Action, BatchActionResults)
	}
	if req.BatchID != "batch_123" {
		t.Fatalf("BatchMetadata.BatchID = %q, want batch_123", req.BatchID)
	}
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
			if tt.wantModel != gotModel || tt.wantProvider != gotProvider || tt.wantStream != gotStream || tt.wantParsed != gotParsed {
				t.Fatalf("gjson mismatch: want (%q, %q, %v, %v), got (%q, %q, %v, %v)", tt.wantModel, tt.wantProvider, tt.wantStream, tt.wantParsed, gotModel, gotProvider, gotStream, gotParsed)
			}
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
