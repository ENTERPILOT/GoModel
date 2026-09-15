package kilo

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

// Kilo is a thin wrapper over the shared chat-centric adapter, so the shared
// contract covers its surface. Kilo exposes no embeddings endpoint, so
// Embeddings must fail fast without an upstream call, and the provider must
// not advertise native batch, file, or audio support.
func TestChatCompatibleContract(t *testing.T) {
	providertest.AssertChatCompatible(t, providertest.ChatCompatible{
		Registration:   Registration,
		Type:           "kilo",
		DefaultBaseURL: "https://api.kilo.ai/api/gateway",
		New: func(apiKey, baseURL string, client *http.Client, hooks llmclient.Hooks) core.Provider {
			return NewWithHTTPClient(apiKey, baseURL, client, hooks)
		},
	})
	providertest.AssertNoNativeSurfaces(t, NewWithHTTPClient("kilo-key", "", nil, llmclient.Hooks{}))
}

func TestChatCompletion_PreservesToolsAndToolChoice(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{
		"id":"chatcmpl-kilo",
		"created":1677652288,
		"model":"anthropic/claude-sonnet-4.5",
		"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}],
		"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}
	}`)

	provider := NewWithHTTPClient("kilo-key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "anthropic/claude-sonnet-4.5",
		Messages: []core.Message{{Role: "user", Content: "Use the tool"}},
		Tools: []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name":       "lookup",
				"parameters": map[string]any{"type": "object"},
			},
		}},
		ToolChoice: "auto",
	})
	require.NoError(t, err)

	sent := capture.Last(t).JSON(t)
	assert.Equal(t, "anthropic/claude-sonnet-4.5", sent["model"])
	assert.Equal(t, "auto", sent["tool_choice"])
	assert.Len(t, sent["tools"], 1)
	require.Len(t, resp.Choices, 1)
	assert.Len(t, resp.Choices[0].Message.ToolCalls, 1)
}

func TestStreamChatCompletion_PreservesStreamOptions(t *testing.T) {
	server, capture := providertest.SSEServer(t, "data: {\"id\":\"chatcmpl-kilo\",\"object\":\"chat.completion.chunk\",\"choices\":[]}\n\ndata: [DONE]\n\n")

	provider := NewWithHTTPClient("kilo-key", server.URL, server.Client(), llmclient.Hooks{})
	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:         "openai/gpt-5.5",
		Messages:      []core.Message{{Role: "user", Content: "hi"}},
		StreamOptions: &core.StreamOptions{IncludeUsage: true},
	})
	require.NoError(t, err)
	defer stream.Close()

	body, err := io.ReadAll(stream)
	require.NoError(t, err)
	assert.Contains(t, string(body), "data: [DONE]")

	sent := capture.Last(t).JSON(t)
	assert.Equal(t, true, sent["stream"])
	assert.Equal(t, map[string]any{"include_usage": true}, sent["stream_options"])
}
