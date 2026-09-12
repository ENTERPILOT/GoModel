package core

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
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
		{path: "/v1/conversations", managed: true, dialect: "openai_compat", operation: OperationConversations, bodyMode: BodyModeNone, interaction: true},
		{path: "/v1/conversations/conv_1", managed: true, dialect: "openai_compat", operation: OperationConversations, bodyMode: BodyModeNone, interaction: true},
		{path: "/v1/conversations/conv_1/items", managed: true, dialect: "openai_compat", operation: OperationConversations, bodyMode: BodyModeNone, interaction: true},
		{path: "/v1/conversations/conv_1/items/msg_1", managed: true, dialect: "openai_compat", operation: OperationConversations, bodyMode: BodyModeNone, interaction: true},
		{path: "/v1/batches", managed: true, dialect: "openai_compat", operation: OperationBatches, bodyMode: BodyModeNone, interaction: true},
		{path: "/v1/messages/batches", managed: true, dialect: "anthropic", operation: OperationBatches, bodyMode: BodyModeNone, interaction: true},
		{path: "/v1/messages/batches/msgbatch_1/results", managed: true, dialect: "anthropic", operation: OperationBatches, bodyMode: BodyModeNone, interaction: true},
		{path: "/v1/embeddings/", managed: true, dialect: "openai_compat", operation: OperationEmbeddings, bodyMode: BodyModeJSON, interaction: true},
		{path: "/v1/files/file_1", managed: true, dialect: "openai_compat", operation: OperationFiles, bodyMode: BodyModeNone, interaction: true},
		{path: "/v1/audio/speech", managed: false, dialect: "openai_compat", operation: OperationAudioSpeech, bodyMode: BodyModeJSON, interaction: true},
		{path: "/v1/audio/transcriptions", managed: false, dialect: "openai_compat", operation: OperationAudioTranscriptions, bodyMode: BodyModeMultipart, interaction: true},
		{path: "/v1/audio/translations", managed: false, dialect: "openai_compat", operation: OperationAudioTranslations, bodyMode: BodyModeMultipart, interaction: true},
		{path: "/v1/images/generations", managed: false, dialect: "openai_compat", operation: OperationImageGenerations, bodyMode: BodyModeJSON, interaction: true},
		{path: "/v1/images/edits", managed: false, dialect: "openai_compat", operation: OperationImageEdits, bodyMode: BodyModeMultipart, interaction: true},
		{path: "/v1/realtime", managed: false, dialect: "openai_compat", operation: OperationRealtime, bodyMode: BodyModeNone, interaction: true},
		{path: "/v1/realtime/calls", managed: false, dialect: "openai_compat", operation: OperationRealtime, bodyMode: BodyModeNone, interaction: true},
		{path: "/v1/realtime/client_secrets", managed: false, dialect: "openai_compat", operation: OperationRealtime, bodyMode: BodyModeNone, interaction: true},
		{path: "/v1/realtime/translations", managed: false, dialect: "openai_compat", operation: OperationRealtime, bodyMode: BodyModeNone, interaction: true},
		{path: "/v1/realtime/translations/calls", managed: false, dialect: "openai_compat", operation: OperationRealtime, bodyMode: BodyModeNone, interaction: true},
		{path: "/v1/realtime/translations/client_secrets", managed: false, dialect: "openai_compat", operation: OperationRealtime, bodyMode: BodyModeNone, interaction: true},
		{path: "/mcp", managed: false, dialect: "mcp", operation: OperationMCP, bodyMode: BodyModeNone, interaction: true},
		{path: "/mcp/linear", managed: false, dialect: "mcp", operation: OperationMCP, bodyMode: BodyModeNone, interaction: true},
		{path: "/p/openai/responses", managed: true, dialect: "provider_passthrough", operation: OperationProviderPassthrough, bodyMode: BodyModeOpaque, interaction: true},
		{path: "/v1/models", managed: false, dialect: "", operation: "", bodyMode: BodyModeNone, interaction: false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := DescribeEndpointPath(tt.path)
			require.Equal(t, tt.interaction, got.ModelInteraction)
			require.Equal(t, tt.managed, got.IngressManaged)
			require.Equal(t, tt.dialect, got.Dialect)
			require.Equal(t, tt.operation, got.Operation)
			require.Equal(t, tt.bodyMode, got.BodyMode)
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
		{method: http.MethodPost, path: "/v1/conversations", bodyMode: BodyModeJSON},
		{method: http.MethodPost, path: "/v1/conversations/conv_1", bodyMode: BodyModeJSON},
		{method: http.MethodGet, path: "/v1/conversations/conv_1", bodyMode: BodyModeNone},
		{method: http.MethodDelete, path: "/v1/conversations/conv_1", bodyMode: BodyModeNone},
		{method: http.MethodPost, path: "/v1/conversations/conv_1/items", bodyMode: BodyModeJSON},
		{method: http.MethodGet, path: "/v1/conversations/conv_1/items", bodyMode: BodyModeNone},
		{method: http.MethodGet, path: "/v1/conversations/conv_1/items/msg_1", bodyMode: BodyModeNone},
		{method: http.MethodDelete, path: "/v1/conversations/conv_1/items/msg_1", bodyMode: BodyModeNone},
		{method: http.MethodPost, path: "/v1/files", bodyMode: BodyModeMultipart},
		{method: http.MethodPost, path: "/v1/files/", bodyMode: BodyModeMultipart},
		{method: http.MethodGet, path: "/v1/files/file_1", bodyMode: BodyModeNone},
		{method: http.MethodPost, path: "/v1/audio/speech", bodyMode: BodyModeJSON},
		{method: http.MethodPost, path: "/v1/audio/transcriptions", bodyMode: BodyModeMultipart},
		{method: http.MethodPost, path: "/v1/audio/translations", bodyMode: BodyModeMultipart},
		{method: http.MethodPost, path: "/v1/images/generations", bodyMode: BodyModeJSON},
		{method: http.MethodPost, path: "/v1/images/edits", bodyMode: BodyModeMultipart},
		{method: http.MethodPost, path: "/v1/batches/batch_1/cancel", bodyMode: BodyModeNone},
		{method: http.MethodGet, path: "/v1/realtime", bodyMode: BodyModeNone},
		{method: http.MethodGet, path: "/v1/realtime/translations", bodyMode: BodyModeNone},
		{method: http.MethodPost, path: "/v1/realtime/calls", bodyMode: BodyModeOpaque},
		{method: http.MethodPost, path: "/v1/realtime/translations/calls", bodyMode: BodyModeOpaque},
		{method: http.MethodPost, path: "/v1/realtime/client_secrets", bodyMode: BodyModeJSON},
		{method: http.MethodPost, path: "/v1/realtime/translations/client_secrets", bodyMode: BodyModeJSON},
		{method: http.MethodPost, path: "/mcp", bodyMode: BodyModeJSON},
		{method: http.MethodGet, path: "/mcp", bodyMode: BodyModeNone},
		{method: http.MethodDelete, path: "/mcp", bodyMode: BodyModeNone},
		{method: http.MethodPost, path: "/mcp/linear", bodyMode: BodyModeJSON},
		{method: http.MethodGet, path: "/mcp/linear", bodyMode: BodyModeNone},
		{method: http.MethodDelete, path: "/mcp/linear", bodyMode: BodyModeNone},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			got := DescribeEndpoint(tt.method, tt.path)
			require.Equal(t, tt.bodyMode, got.BodyMode)
		})
	}
}

func TestParseProviderPassthroughPath(t *testing.T) {
	provider, endpoint, ok := ParseProviderPassthroughPath("/p/anthropic/messages/batches")
	require.True(t, ok)
	require.Equal(t, "anthropic", provider)
	require.Equal(t, "messages/batches", endpoint)
}
