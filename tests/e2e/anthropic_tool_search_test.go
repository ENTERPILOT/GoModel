//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// A conversation that ran Anthropic's server-side tool search on Claude and is
// continued on another model through /v1/messages. The assistant turn is what
// Anthropic returns for the regex and BM25 searches.
func TestMessagesReplayServerToolSearch(t *testing.T) {
	tests := []struct {
		name       string
		toolType   string
		toolName   string
		input      string
		wantSearch string
	}{
		{
			name:       "regex",
			toolType:   "tool_search_tool_regex_20251119",
			toolName:   "tool_search_tool_regex",
			input:      `{"pattern":"weather","limit":10}`,
			wantSearch: "Tool search: weather",
		},
		{
			name:       "bm25",
			toolType:   "tool_search_tool_bm25_20251119",
			toolName:   "tool_search_tool_bm25",
			input:      `{"query":"weather in Paris","limit":5}`,
			wantSearch: "Tool search: weather in Paris",
		},
	}
	for _, tc := range tests {
		body := `{
			"model":"gpt-4.1","max_tokens":100,
			"tools":[
				{"type":"` + tc.toolType + `","name":"` + tc.toolName + `"},
				{"name":"get_weather","description":"Get the current weather for a city","input_schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]},"defer_loading":true}
			],
			"messages":[
				{"role":"user","content":"What is the weather in Paris? Use your tools."},
				{"role":"assistant","content":[
					{"type":"text","text":"I'll search for a weather tool."},
					{"type":"server_tool_use","id":"srvtoolu_01","name":"` + tc.toolName + `","input":` + tc.input + `},
					{"type":"tool_search_tool_result","tool_use_id":"srvtoolu_01","content":{
						"type":"tool_search_tool_search_result",
						"tool_references":[{"type":"tool_reference","tool_name":"get_weather"}]
					}},
					{"type":"tool_use","id":"toolu_01","name":"get_weather","input":{"city":"Paris"}}
				]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01","content":"18C and sunny"}]}
			]
		}`

		t.Run(tc.name, func(t *testing.T) {
			mockServer.ResetRequests()

			resp := sendMessagesRequest(t, "/v1/messages", body)
			defer closeBody(resp)
			raw, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", raw)
			var message struct {
				Type string `json:"type"`
			}
			require.NoError(t, json.Unmarshal(raw, &message))
			assert.Equal(t, "message", message.Type)

			upstream := requireRecordedChatRequest(t)
			require.Len(t, upstream.Tools, 1, "the tool search tool is dropped; the deferred tool is sent in full")
			fn, ok := upstream.Tools[0]["function"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, "get_weather", fn["name"])
			assert.NotContains(t, fn, "defer_loading")

			require.Len(t, upstream.Messages, 3)
			assistant := upstream.Messages[1]
			assert.Equal(t, "assistant", assistant.Role)
			text := core.ExtractTextContent(assistant.Content)
			assert.Contains(t, text, tc.wantSearch)
			assert.Contains(t, text, "Tools found: get_weather")
			require.Len(t, assistant.ToolCalls, 1)
			assert.Equal(t, "get_weather", assistant.ToolCalls[0].Function.Name)
			assert.Equal(t, "tool", upstream.Messages[2].Role)
		})

		t.Run(tc.name+" streaming", func(t *testing.T) {
			resp := sendMessagesRequest(t, "/v1/messages", `{"stream":true,`+body[1:])
			defer closeBody(resp)
			raw, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", raw)
			assert.Contains(t, string(raw), "event: message_stop")
		})

		t.Run(tc.name+" count_tokens", func(t *testing.T) {
			resp := sendMessagesRequest(t, "/v1/messages/count_tokens", body)
			defer closeBody(resp)
			raw, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", raw)
			var count struct {
				InputTokens int `json:"input_tokens"`
			}
			require.NoError(t, json.Unmarshal(raw, &count))
			assert.Positive(t, count.InputTokens)
		})
	}

	t.Run("other server tools stay rejected", func(t *testing.T) {
		mockServer.ResetRequests()

		resp := sendMessagesRequest(t, "/v1/messages", `{
			"model":"gpt-4.1","max_tokens":100,
			"messages":[
				{"role":"user","content":"search the web"},
				{"role":"assistant","content":[
					{"type":"server_tool_use","id":"srvtoolu_02","name":"web_search","input":{"query":"q"}},
					{"type":"text","text":"found it"}
				]},
				{"role":"user","content":"thanks"}
			]
		}`)
		defer closeBody(resp)
		raw, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode, "body: %s", raw)
		assert.Contains(t, string(raw), `unsupported content block type \"server_tool_use\"`)
		assert.Empty(t, mockServer.Requests(), "a rejected request never reaches the provider")
	})
}

// sendMessagesRequest posts a raw Anthropic Messages body to path.
func sendMessagesRequest(t *testing.T, path, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, gatewayURL+path, bytes.NewReader([]byte(body)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}
