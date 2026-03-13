package core

import (
	"net/http"
	"testing"
)

func TestBuildSemanticEnvelope_OpenAICompat(t *testing.T) {
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
	if env.CachedChatRequest() != nil || env.CachedResponsesRequest() != nil || env.CachedEmbeddingRequest() != nil || env.CachedBatchRequest() != nil || env.CachedBatchRouteInfo() != nil || env.CachedFileRouteInfo() != nil {
		t.Fatalf("canonical request payloads should be nil, got %+v", env)
	}
}

func TestBuildSemanticEnvelope_InvalidJSONRemainsPartial(t *testing.T) {
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

func TestBuildSemanticEnvelope_PassthroughRouteParams(t *testing.T) {
	frame := NewRequestSnapshot(
		"POST",
		"/p/openai/responses",
		map[string]string{"provider": "openai", "endpoint": "responses"},
		nil,
		nil,
		"",
		[]byte(`{"model":"gpt-5-mini","foo":"bar"}`),
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
	if env.CachedChatRequest() != nil || env.CachedResponsesRequest() != nil || env.CachedEmbeddingRequest() != nil || env.CachedBatchRequest() != nil || env.CachedBatchRouteInfo() != nil || env.CachedFileRouteInfo() != nil {
		t.Fatalf("canonical request payloads should be nil, got %+v", env)
	}
}

func TestBuildSemanticEnvelope_PassthroughPathFallback(t *testing.T) {
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
}

func TestBuildSemanticEnvelope_SkipsBodyParsingWhenIngressBodyWasNotCaptured(t *testing.T) {
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

func TestBuildSemanticEnvelope_FilesMetadata(t *testing.T) {
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

func TestBuildSemanticEnvelope_BatchesListMetadata(t *testing.T) {
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

func TestBuildSemanticEnvelope_BatchResultsMetadata(t *testing.T) {
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
