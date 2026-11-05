package sglang

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

const (
	responsesPath = "/v1/responses"
	chatPath      = "/v1/chat/completions"
)

func TestParseAPIMode(t *testing.T) {
	tests := []struct {
		value   string
		want    string
		wantErr bool
	}{
		{value: "", want: apiModeAuto},
		{value: "auto", want: apiModeAuto},
		{value: " Native ", want: apiModeNative},
		{value: "CHAT_COMPATIBLE", want: apiModeChatCompatible},
		{value: "chat", wantErr: true},
		{value: "openai_compatible", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			got, err := parseAPIMode(tt.value)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "api_mode must be auto, native, or chat_compatible")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNewWithUnknownAPIModeFailsEveryRequest(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ModelsJSON)
	provider := newModeTestProvider(t, "bogus", server.URL+"/v1", server.Client())

	_, err := provider.ListModels(context.Background())
	assertConfigError(t, err)
	_, err = provider.Responses(context.Background(), responsesRequest(nil, false))
	assertConfigError(t, err)
	_, err = provider.StreamResponses(context.Background(), responsesRequest(nil, true))
	assertConfigError(t, err)
	_, err = provider.ChatCompletion(context.Background(), &core.ChatRequest{Model: providertest.Model})
	assertConfigError(t, err)
	assert.Zero(t, capture.Count(), "a misconfigured provider must not call upstream")
}

func TestResponsesRoutesByAPIMode(t *testing.T) {
	functionTool := map[string]any{"type": "function", "name": "get_weather", "parameters": map[string]any{"type": "object"}}
	hostedTool := map[string]any{"type": "web_search_preview"}
	tools := map[string][]map[string]any{
		"no tools":        nil,
		"function tool":   {functionTool},
		"hosted tool":     {hostedTool},
		"function+hosted": {functionTool, hostedTool},
	}
	tests := []struct {
		mode     string
		wantPath map[string]string
	}{
		{mode: "", wantPath: map[string]string{
			"no tools": responsesPath, "function tool": chatPath, "hosted tool": responsesPath, "function+hosted": chatPath,
		}},
		{mode: apiModeNative, wantPath: map[string]string{
			"no tools": responsesPath, "function tool": responsesPath, "hosted tool": responsesPath, "function+hosted": responsesPath,
		}},
		{mode: apiModeChatCompatible, wantPath: map[string]string{
			"no tools": chatPath, "function tool": chatPath, "hosted tool": chatPath, "function+hosted": chatPath,
		}},
	}

	for _, tt := range tests {
		for toolCase, wantPath := range tt.wantPath {
			for _, stream := range []bool{false, true} {
				name := "mode=" + tt.mode + "/" + toolCase
				if stream {
					name += "/stream"
				}
				t.Run(name, func(t *testing.T) {
					server, capture := modeTestServer(t)
					provider := newModeTestProvider(t, tt.mode, server.URL+"/v1", server.Client())
					req := responsesRequest(tools[toolCase], stream)

					if stream {
						body, err := provider.StreamResponses(context.Background(), req)
						require.NoError(t, err)
						raw, err := io.ReadAll(body)
						require.NoError(t, err)
						require.NoError(t, body.Close())
						assert.Contains(t, string(raw), `"type":"response.created"`)
						assert.Contains(t, string(raw), `"delta":"`+providertest.Reply+`"`)
						assert.Contains(t, string(raw), "data: [DONE]")
					} else {
						resp, err := provider.Responses(context.Background(), req)
						require.NoError(t, err)
						assert.Equal(t, "response", resp.Object)
						require.NotEmpty(t, resp.Output)
						require.NotEmpty(t, resp.Output[0].Content)
						assert.Equal(t, providertest.Reply, resp.Output[0].Content[0].Text)
					}

					sent := capture.Last(t)
					assert.Equal(t, wantPath, sent.Path)
					if wantPath == chatPath {
						assertChatTools(t, sent.JSON(t), tools[toolCase])
					}
				})
			}
		}
	}
}

// TestResponsesViaChatCarriesExpandedHistory covers a chained Codex turn: the
// gateway expands previous_response_id into input before dispatch (SGLang has
// no native response lifecycle), so the prior tool call and its output reach
// chat completions as assistant tool_calls and a tool message.
func TestResponsesViaChatCarriesExpandedHistory(t *testing.T) {
	server, capture := modeTestServer(t)
	provider := newModeTestProvider(t, "", server.URL+"/v1", server.Client())

	var req core.ResponsesRequest
	require.NoError(t, json.Unmarshal([]byte(`{
		"model":"`+providertest.Model+`",
		"tools":[{"type":"function","name":"get_weather","parameters":{"type":"object"}}],
		"input":[
			{"type":"message","role":"user","content":"weather in Paris?"},
			{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_1","output":"sunny"},
			{"type":"message","role":"user","content":"and tomorrow?"}
		]
	}`), &req))

	_, err := provider.Responses(context.Background(), &req)
	require.NoError(t, err)

	sent := capture.Last(t)
	require.Equal(t, chatPath, sent.Path)
	messages, ok := sent.JSON(t)["messages"].([]any)
	require.True(t, ok)
	roles := make([]any, 0, len(messages))
	for _, message := range messages {
		roles = append(roles, message.(map[string]any)["role"])
	}
	assert.Equal(t, []any{"user", "assistant", "tool", "user"}, roles)
}

// An id the gateway did not expand (no response store) cannot be honored over
// chat completions; it fails as a clear invalid request, not an upstream 500.
func TestResponsesViaChatRejectsUnexpandedPreviousResponseID(t *testing.T) {
	server, capture := modeTestServer(t)
	provider := newModeTestProvider(t, apiModeChatCompatible, server.URL+"/v1", server.Client())
	req := responsesRequest(nil, false)
	req.PreviousResponseID = "resp_1"

	_, err := provider.Responses(context.Background(), req)
	providertest.AssertUnsupported(t, err)
	assert.Contains(t, err.Error(), "previous_response_id")
	assert.Zero(t, capture.Count())
}

func newModeTestProvider(t *testing.T, apiMode, baseURL string, client *http.Client) *Provider {
	t.Helper()
	opts := providertest.Options(llmclient.Hooks{})
	opts.HTTPClient = client
	provider, ok := New(providers.ProviderConfig{BaseURL: baseURL, APIMode: apiMode}, opts).(*Provider)
	require.True(t, ok)
	return provider
}

// modeTestServer answers both SGLang endpoints with the shared fixtures,
// streaming when the request asks for it.
func modeTestServer(t *testing.T) (*httptest.Server, *providertest.Capture) {
	t.Helper()
	reply := func(jsonBody, sseBody string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Stream bool `json:"stream"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Stream {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, sseBody)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, jsonBody)
		}
	}
	return providertest.RouteServer(t, map[string]http.HandlerFunc{
		responsesPath: reply(providertest.ResponsesJSON, providertest.ResponsesSSE),
		chatPath:      reply(providertest.ChatCompletionJSON, providertest.ChatChunkSSE),
	})
}

func responsesRequest(tools []map[string]any, stream bool) *core.ResponsesRequest {
	return &core.ResponsesRequest{Model: providertest.Model, Input: providertest.Prompt, Tools: tools, Stream: stream}
}

// assertChatTools checks that only the function tools reach chat completions,
// in chat's nested function shape; hosted tools have no chat equivalent.
func assertChatTools(t *testing.T, sent map[string]any, tools []map[string]any) {
	t.Helper()
	var wantNames []any
	for _, tool := range tools {
		if tool["type"] == "function" {
			wantNames = append(wantNames, tool["name"])
		}
	}
	sentTools, _ := sent["tools"].([]any)
	var gotNames []any
	for _, tool := range sentTools {
		function, _ := tool.(map[string]any)["function"].(map[string]any)
		gotNames = append(gotNames, function["name"])
	}
	assert.Equal(t, wantNames, gotNames)
}

func assertConfigError(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `api_mode must be auto, native, or chat_compatible, got "bogus"`)
}
