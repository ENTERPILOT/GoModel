package providertest

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// allowedToolsRequest declares two functions and allows only the second.
func allowedToolsRequest() *core.ChatRequest {
	function := func(name string) map[string]any {
		return map[string]any{"type": "function", "function": map[string]any{
			"name":       name,
			"parameters": map[string]any{"type": "object", "properties": map[string]any{}},
		}}
	}
	return &core.ChatRequest{
		Model:    Model,
		Messages: []core.Message{{Role: "user", Content: Prompt}},
		Tools:    []map[string]any{function("get_weather"), function("get_time")},
		ToolChoice: map[string]any{"type": "allowed_tools", "allowed_tools": map[string]any{
			"mode":  "required",
			"tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "get_time"}}},
		}},
	}
}

// AssertAllowedTools checks how a provider sends an allowed_tools
// tool_choice on chat and streaming chat. With forwards set it must reach the
// upstream unchanged; otherwise the declared tools must be narrowed to the
// allowed subset, so an upstream that ignores the choice cannot call a tool
// outside it. newProvider builds the provider on the test server's URL and
// client.
func AssertAllowedTools(t *testing.T, forwards bool, newProvider func(baseURL string, client *http.Client) core.Provider) {
	t.Helper()
	for _, stream := range []bool{false, true} {
		name := "chat"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			var capture *Capture
			if stream {
				server, c := SSEServer(t, ChatChunkSSE)
				body, err := newProvider(server.URL, server.Client()).StreamChatCompletion(context.Background(), allowedToolsRequest())
				require.NoError(t, err)
				readAll(t, body)
				capture = c
			} else {
				server, c := JSONServer(t, http.StatusOK, ChatCompletionJSON)
				_, err := newProvider(server.URL, server.Client()).ChatCompletion(context.Background(), allowedToolsRequest())
				require.NoError(t, err)
				capture = c
			}
			sent := capture.Last(t).JSON(t)
			tools, _ := sent["tools"].([]any)
			if forwards {
				assert.Len(t, tools, 2, "declared tools must reach the upstream unchanged")
				choice, _ := sent["tool_choice"].(map[string]any)
				assert.Equal(t, "allowed_tools", choice["type"], "tool_choice = %#v", sent["tool_choice"])
				return
			}
			require.Len(t, tools, 1, "declared tools must be narrowed to the allowed subset")
			tool, _ := tools[0].(map[string]any)
			fn, _ := tool["function"].(map[string]any)
			assert.Equal(t, "get_time", fn["name"])
			assert.Equal(t, "required", sent["tool_choice"])
		})
	}
}
