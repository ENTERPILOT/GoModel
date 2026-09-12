package opencodego

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestProvider builds a provider whose OpenAI-compatible and Anthropic
// /messages paths both point at the same test server.
func newTestProvider(serverURL string, client *http.Client) *Provider {
	return NewWithHTTPClient("sk-opencode", serverURL, client, llmclient.Hooks{})
}

func TestChatCompatibleContract(t *testing.T) {
	providertest.AssertChatCompatible(t, providertest.ChatCompatible{
		Registration:   Registration,
		Type:           "opencode_go",
		DefaultBaseURL: "https://opencode.ai/zen/go/v1",
		New: func(apiKey, baseURL string, client *http.Client, hooks llmclient.Hooks) core.Provider {
			return NewWithHTTPClient(apiKey, baseURL, client, hooks)
		},
		Embeddings: false,
	})
	providertest.AssertNoNativeSurfaces(t, newTestProvider("", nil))
}

func TestChatCompletion_AnthropicStyleModel_UsesMessages(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{
		"id":"msg_opencode",
		"model":"qwen3.7-max",
		"content":[{"type":"text","text":"hello"}],
		"stop_reason":"end_turn",
		"usage":{"input_tokens":5,"output_tokens":2}
	}`)

	resp, err := newTestProvider(server.URL, server.Client()).ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "qwen3.7-max",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)

	req := capture.Last(t)
	assert.Equal(t, "/messages", req.Path)
	assert.Equal(t, "sk-opencode", req.Header.Get("x-api-key"))
	assert.Empty(t, req.Header.Get("Authorization"))
	assert.NotEmpty(t, req.Header.Get("anthropic-version"))
	require.Len(t, resp.Choices, 1)
	assert.Equal(t, "hello", resp.Choices[0].Message.Content)
}

func TestStreamChatCompletion_RoutesByModel(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		wantPath string
	}{
		{"openai-style", "glm-5.1", "/chat/completions"},
		{"anthropic-style", "qwen3.7-max", "/messages"},
		{"prefixed anthropic-style", "opencode_go/qwen3.7-max", "/messages"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.SSEServer(t, "data: [DONE]\n\n")

			stream, err := newTestProvider(server.URL, server.Client()).StreamChatCompletion(context.Background(), &core.ChatRequest{
				Model:    tt.model,
				Messages: []core.Message{{Role: "user", Content: "hi"}},
			})
			require.NoError(t, err)

			_, _ = io.Copy(io.Discard, stream)
			_ = stream.Close()
			assert.Equal(t, tt.wantPath, capture.Last(t).Path)
		})
	}
}

func TestMessagesModels_EnvOverride(t *testing.T) {
	t.Setenv(messagesModelsEnvVar, "foo-model, bar-model ")
	p := newTestProvider("", nil)

	assert.True(t, p.usesMessages("foo-model"))
	assert.True(t, p.usesMessages("bar-model"))
	assert.False(t, p.usesMessages("qwen3.7-max"))
	assert.True(t, p.usesMessages("opencode_go/foo-model"))
}
