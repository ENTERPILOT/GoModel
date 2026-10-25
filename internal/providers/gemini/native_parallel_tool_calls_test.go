package gemini

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

func TestStreamChatCompletion_NativeParallelToolCallsAcrossEvents(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server, _ := providertest.SSEServer(t, `data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_1","name":"lookup_weather","args":{"city":"Warsaw"}},"thoughtSignature":"sig-1"}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_2","name":"lookup_weather","args":{"city":"Krakow"}}}]},"finishReason":"STOP"}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP"}]}

`)

	provider := newTestProvider("test-api-key", nil, llmclient.Hooks{})
	provider.SetModelsURL(server.URL)

	body, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "gemini-3.5-flash",
		Messages: []core.Message{{Role: "user", Content: "Weather in Warsaw and Krakow?"}},
	})
	require.NoError(t, err)

	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	require.NoError(t, err)

	toolCalls, finishReasons := collectStreamToolCalls(t, string(raw))
	require.Len(t, toolCalls, 2, "stream %s", raw)
	assert.Equal(t, "call_1", toolCalls[0]["id"])
	assert.InDelta(t, 0, toolCalls[0]["index"], 0)
	assert.Equal(t, "call_2", toolCalls[1]["id"])
	assert.InDelta(t, 1, toolCalls[1]["index"], 0)
	assert.Equal(t, []any{"tool_calls"}, finishReasons)
}

func TestStreamChatCompletion_NativeIDLessToolCallsReplay(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server, _ := providertest.SSEServer(t, `data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"lookup_weather","args":{"city":"Warsaw"}},"thoughtSignature":"sig-1"}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"lookup_time","args":{"city":"Warsaw"}}}]},"finishReason":"STOP"}]}

`)

	provider := newTestProvider("test-api-key", nil, llmclient.Hooks{})
	provider.SetModelsURL(server.URL)

	body, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "gemini-3.5-flash",
		Messages: []core.Message{{Role: "user", Content: "Weather and time in Warsaw?"}},
	})
	require.NoError(t, err)

	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	require.NoError(t, err)

	streamed, _ := collectStreamToolCalls(t, string(raw))
	require.Len(t, streamed, 2, "stream %s", raw)

	calls := make([]core.ToolCall, 0, len(streamed))
	messages := []core.Message{{Role: "user", Content: "Weather and time in Warsaw?"}, {Role: "assistant"}}
	for _, call := range streamed {
		id, ok := call["id"].(string)
		require.True(t, ok, "tool call = %#v", call)
		function, ok := call["function"].(map[string]any)
		require.True(t, ok, "tool call = %#v", call)
		name, ok := function["name"].(string)
		require.True(t, ok, "tool call = %#v", call)
		calls = append(calls, core.ToolCall{ID: id, Type: "function", Function: core.FunctionCall{Name: name, Arguments: `{"city":"Warsaw"}`}})
		messages = append(messages, core.Message{Role: "tool", ToolCallID: id, Content: `{"ok":true}`})
	}
	messages[1].ToolCalls = calls
	assert.NotEqual(t, calls[0].ID, calls[1].ID)

	req, err := convertChatRequestToGemini(&core.ChatRequest{Model: "gemini-3.5-flash", Messages: messages})
	require.NoError(t, err)

	require.Len(t, req.Contents, 3, "contents = %+v", req.Contents)
	results := req.Contents[2].Parts
	require.Len(t, results, 2, "parts = %+v", results)
	require.NotNil(t, results[0].FunctionResponse)
	assert.Equal(t, "lookup_weather", results[0].FunctionResponse.Name)
	require.NotNil(t, results[1].FunctionResponse)
	assert.Equal(t, "lookup_time", results[1].FunctionResponse.Name)
}

func TestStreamChatCompletion_NativeMixedToolCallIDsStayUnique(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server, _ := providertest.SSEServer(t, `data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_1","name":"lookup_weather","args":{}}}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"lookup_time","args":{}}},{"functionCall":{"id":"call_0","name":"lookup_news","args":{}}}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_1","name":"lookup_tide","args":{}}}]},"finishReason":"STOP"}]}

`)

	provider := newTestProvider("test-api-key", nil, llmclient.Hooks{})
	provider.SetModelsURL(server.URL)

	body, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "gemini-3.5-flash",
		Messages: []core.Message{{Role: "user", Content: "Weather, time, news and tide?"}},
	})
	require.NoError(t, err)

	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	require.NoError(t, err)

	toolCalls, _ := collectStreamToolCalls(t, string(raw))
	ids := make([]any, 0, len(toolCalls))
	for _, call := range toolCalls {
		ids = append(ids, call["id"])
	}
	assert.Equal(t, []any{"call_1", "call_2", "call_0", "call_3"}, ids, "stream %s", raw)
}

func TestConvertChatRequestToGemini_GroupsParallelToolResults(t *testing.T) {
	req, err := convertChatRequestToGemini(&core.ChatRequest{
		Model: "gemini-3.5-flash",
		Messages: []core.Message{
			{Role: "user", Content: "Weather in Warsaw and Krakow?"},
			{Role: "assistant", ToolCalls: []core.ToolCall{
				toolCallWithSignature("call_1", "lookup_weather", "sig-1"),
				{ID: "call_2", Type: "function", Function: core.FunctionCall{Name: "lookup_weather", Arguments: `{"city":"Krakow"}`}},
			}},
			{Role: "tool", ToolCallID: "call_1", Content: `{"temp":12}`},
			{Role: "tool", ToolCallID: "call_2", Content: `{"temp":14}`},
			{Role: "user", Content: "Thanks"},
		},
	})
	require.NoError(t, err)

	require.Len(t, req.Contents, 4, "contents = %+v", req.Contents)
	results := req.Contents[2]
	assert.Equal(t, "user", results.Role)
	require.Len(t, results.Parts, 2, "parts = %+v", results.Parts)
	require.NotNil(t, results.Parts[0].FunctionResponse)
	assert.Equal(t, "call_1", results.Parts[0].FunctionResponse.ID)
	assert.Equal(t, "lookup_weather", results.Parts[0].FunctionResponse.Name)
	require.NotNil(t, results.Parts[1].FunctionResponse)
	assert.Equal(t, "call_2", results.Parts[1].FunctionResponse.ID)
	assert.Equal(t, []geminiPart{{Text: "Thanks"}}, req.Contents[3].Parts)
}

func collectStreamToolCalls(t *testing.T, stream string) ([]map[string]any, []any) {
	t.Helper()

	chunks := parseOpenAIStreamChunks(t, stream)
	var toolCalls []map[string]any
	var finishReasons []any
	for i, chunk := range chunks {
		choices, ok := chunk["choices"].([]any)
		require.True(t, ok, "chunk %d = %#v", i, chunk)
		require.Len(t, choices, 1, "chunk %d = %#v", i, chunk)
		choice, ok := choices[0].(map[string]any)
		require.True(t, ok, "chunk %d = %#v", i, chunk)
		if finish := choice["finish_reason"]; finish != nil {
			require.Equal(t, len(chunks)-1, i, "finish_reason before the last chunk: %s", stream)
			finishReasons = append(finishReasons, finish)
		}
		delta, ok := choice["delta"].(map[string]any)
		require.True(t, ok, "chunk %d = %#v", i, chunk)
		calls, _ := delta["tool_calls"].([]any)
		for _, call := range calls {
			toolCall, ok := call.(map[string]any)
			require.True(t, ok, "tool call = %#v", call)
			toolCalls = append(toolCalls, toolCall)
		}
	}
	return toolCalls, finishReasons
}
