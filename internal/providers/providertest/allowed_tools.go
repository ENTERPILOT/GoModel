package providertest

import (
	"context"
	"encoding/json"
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
			req := allowedToolsRequest()
			if forwards {
				assert.Equal(t, asJSON(t, req.Tools), sent["tools"], "declared tools must reach the upstream unchanged")
				assert.Equal(t, asJSON(t, req.ToolChoice), sent["tool_choice"], "tool_choice must reach the upstream unchanged")
				return
			}
			assert.Equal(t, asJSON(t, req.Tools[1:]), sent["tools"], "declared tools must be narrowed to the allowed subset")
			assert.Equal(t, "required", sent["tool_choice"])
		})
	}
}

// asJSON round-trips v through JSON so it compares equal to a decoded
// upstream request body.
func asJSON(t testing.TB, v any) any {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	var out any
	require.NoError(t, json.Unmarshal(data, &out))
	return out
}
