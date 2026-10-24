package openai

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

func TestAllowedTools_OpenAIForwards(t *testing.T) {
	providertest.AssertAllowedTools(t, true, func(baseURL string, client *http.Client) core.Provider {
		provider := newHTTPTestProvider("test-key", client, llmclient.Hooks{})
		provider.SetBaseURL(baseURL)
		return provider
	})
}

func TestAllowedTools_CompatibleProvider(t *testing.T) {
	tests := []struct {
		name    string
		forward bool
	}{
		{name: "narrows by default"},
		{name: "forwards when configured", forward: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			providertest.AssertAllowedTools(t, tt.forward, func(baseURL string, client *http.Client) core.Provider {
				return NewChatCompatible("test-key", providers.ProviderOptions{HTTPClient: client}, CompatibleProviderConfig{
					ProviderName:        "compatible",
					BaseURL:             baseURL,
					ForwardAllowedTools: tt.forward,
				})
			})
		})
	}
}

func allowedToolsChoice(names ...string) map[string]any {
	tools := make([]any, 0, len(names))
	for _, name := range names {
		tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": name}})
	}
	return map[string]any{"type": "allowed_tools", "allowed_tools": map[string]any{"mode": "auto", "tools": tools}}
}

func TestAllowedTools_NarrowingKeepsCallerRequest(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
	provider := NewCompatibleProvider("test-key", providers.ProviderOptions{HTTPClient: server.Client()},
		CompatibleProviderConfig{ProviderName: "compatible", BaseURL: server.URL})
	weather := map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}}
	clock := map[string]any{"type": "function", "function": map[string]any{"name": "get_time"}}
	choice := allowedToolsChoice("get_time")
	req := &core.ChatRequest{
		Model:      providertest.Model,
		Messages:   []core.Message{{Role: "user", Content: providertest.Prompt}},
		Tools:      []map[string]any{weather, clock},
		ToolChoice: choice,
	}

	_, err := provider.ChatCompletion(context.Background(), req)
	require.NoError(t, err)

	sent := capture.Last(t).JSON(t)
	assert.Len(t, sent["tools"], 1)
	assert.Equal(t, "auto", sent["tool_choice"])
	assert.Len(t, req.Tools, 2, "the caller's tools must not be mutated")
	assert.Equal(t, choice, req.ToolChoice, "the caller's tool_choice must not be mutated")
}

func TestAllowedTools_NarrowingRejectsUndeclaredSubset(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
	provider := NewCompatibleProvider("test-key", providers.ProviderOptions{HTTPClient: server.Client()},
		CompatibleProviderConfig{ProviderName: "compatible", BaseURL: server.URL})
	req := &core.ChatRequest{
		Model:      providertest.Model,
		Messages:   []core.Message{{Role: "user", Content: providertest.Prompt}},
		Tools:      []map[string]any{{"type": "function", "function": map[string]any{"name": "get_weather"}}},
		ToolChoice: allowedToolsChoice("get_time"),
	}

	_, chatErr := provider.ChatCompletion(context.Background(), req)
	_, streamErr := provider.StreamChatCompletion(context.Background(), req)
	for _, err := range []error{chatErr, streamErr} {
		var gwErr *core.GatewayError
		require.ErrorAs(t, err, &gwErr)
		assert.Equal(t, core.ErrorTypeInvalidRequest, gwErr.Type)
		assert.Contains(t, gwErr.Message, "tool_choice.allowed_tools.tools")
	}
	assert.Zero(t, capture.Count(), "a subset naming no declared tool must not reach the upstream")
}

// A Responses request served through chat completions is narrowed on the way
// out too, since the translation dispatches through the same chat methods.
func TestAllowedTools_ResponsesViaChatNarrows(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
	provider := NewChatCompatible("test-key", providers.ProviderOptions{HTTPClient: server.Client()},
		CompatibleProviderConfig{ProviderName: "compatible", BaseURL: server.URL})
	function := func(name string) map[string]any {
		return map[string]any{"type": "function", "name": name, "parameters": map[string]any{"type": "object"}}
	}

	_, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model: providertest.Model,
		Input: providertest.Prompt,
		Tools: []map[string]any{function("get_weather"), function("get_time")},
		ToolChoice: map[string]any{"type": "allowed_tools", "mode": "required",
			"tools": []any{map[string]any{"type": "function", "name": "get_time"}}},
	})
	require.NoError(t, err)

	sent := capture.Last(t).JSON(t)
	tools, _ := sent["tools"].([]any)
	require.Len(t, tools, 1)
	fn, _ := tools[0].(map[string]any)["function"].(map[string]any)
	assert.Equal(t, "get_time", fn["name"])
	assert.Equal(t, "required", sent["tool_choice"])
}
