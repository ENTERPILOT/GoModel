package core

import (
	"net/http"
	"testing"
)

func TestDescribeEndpointPath(t *testing.T) {
	tests := []struct {
		path        string
		managed     bool
		dialect     string
		operation   Operation
		bodyMode    BodyMode
		interaction bool
	}{
		{path: "/v1/chat/completions", managed: true, dialect: "openai_compat", operation: OperationChatCompletions, bodyMode: BodyModeJSON, interaction: true},
		{path: "/v1/chat/completions/", managed: true, dialect: "openai_compat", operation: OperationChatCompletions, bodyMode: BodyModeJSON, interaction: true},
		{path: "/v1/responses/resp_1", managed: true, dialect: "openai_compat", operation: OperationResponses, bodyMode: BodyModeNone, interaction: true},
		{path: "/v1/responses/resp_1/input_items", managed: true, dialect: "openai_compat", operation: OperationResponses, bodyMode: BodyModeNone, interaction: true},
		{path: "/v1/batches", managed: true, dialect: "openai_compat", operation: OperationBatches, bodyMode: BodyModeNone, interaction: true},
		{path: "/v1/embeddings/", managed: true, dialect: "openai_compat", operation: OperationEmbeddings, bodyMode: BodyModeJSON, interaction: true},
		{path: "/v1/files/file_1", managed: true, dialect: "openai_compat", operation: OperationFiles, bodyMode: BodyModeNone, interaction: true},
		{path: "/p/openai/responses", managed: true, dialect: "provider_passthrough", operation: OperationProviderPassthrough, bodyMode: BodyModeOpaque, interaction: true},
		{path: "/v1/models", managed: false, dialect: "", operation: "", bodyMode: BodyModeNone, interaction: false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := DescribeEndpointPath(tt.path)
			if got.ModelInteraction != tt.interaction {
				t.Fatalf("ModelInteraction = %v, want %v", got.ModelInteraction, tt.interaction)
			}
			if got.IngressManaged != tt.managed {
				t.Fatalf("IngressManaged = %v, want %v", got.IngressManaged, tt.managed)
			}
			if got.Dialect != tt.dialect {
				t.Fatalf("Dialect = %q, want %q", got.Dialect, tt.dialect)
			}
			if got.Operation != tt.operation {
				t.Fatalf("Operation = %q, want %q", got.Operation, tt.operation)
			}
			if got.BodyMode != tt.bodyMode {
				t.Fatalf("BodyMode = %q, want %q", got.BodyMode, tt.bodyMode)
			}
		})
	}
}

func TestDescribeEndpoint_UsesMethodForBodyMode(t *testing.T) {
	tests := []struct {
		method   string
		path     string
		bodyMode BodyMode
	}{
		{method: http.MethodPost, path: "/v1/batches", bodyMode: BodyModeJSON},
		{method: http.MethodGet, path: "/v1/batches", bodyMode: BodyModeNone},
		{method: http.MethodPost, path: "/v1/chat/completions/", bodyMode: BodyModeJSON},
		{method: http.MethodPost, path: "/v1/responses", bodyMode: BodyModeJSON},
		{method: http.MethodGet, path: "/v1/responses/resp_1", bodyMode: BodyModeNone},
		{method: http.MethodGet, path: "/v1/responses/resp_1/input_items", bodyMode: BodyModeNone},
		{method: http.MethodPost, path: "/v1/responses/resp_1/cancel", bodyMode: BodyModeNone},
		{method: http.MethodDelete, path: "/v1/responses/resp_1", bodyMode: BodyModeNone},
		{method: http.MethodPost, path: "/v1/responses/input_tokens", bodyMode: BodyModeJSON},
		{method: http.MethodPost, path: "/v1/responses/compact", bodyMode: BodyModeJSON},
		{method: http.MethodPost, path: "/v1/files", bodyMode: BodyModeMultipart},
		{method: http.MethodPost, path: "/v1/files/", bodyMode: BodyModeMultipart},
		{method: http.MethodGet, path: "/v1/files/file_1", bodyMode: BodyModeNone},
		{method: http.MethodPost, path: "/v1/batches/batch_1/cancel", bodyMode: BodyModeNone},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			got := DescribeEndpoint(tt.method, tt.path)
			if got.BodyMode != tt.bodyMode {
				t.Fatalf("BodyMode = %q, want %q", got.BodyMode, tt.bodyMode)
			}
		})
	}
}

func TestParseProviderPassthroughPath(t *testing.T) {
	provider, endpoint, ok := ParseProviderPassthroughPath("/p/anthropic/messages/batches")
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if provider != "anthropic" {
		t.Fatalf("provider = %q, want anthropic", provider)
	}
	if endpoint != "messages/batches" {
		t.Fatalf("endpoint = %q, want messages/batches", endpoint)
	}
}
