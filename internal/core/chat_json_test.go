package core

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChatRequestJSON_CanonicalizesToolObjectKeyOrder(t *testing.T) {
	bodies := [][]byte{
		[]byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"z":{"type":"string"},"a":{"type":"string"}}}}}]}`),
		[]byte(`{"tools":[{"function":{"parameters":{"properties":{"a":{"type":"string"},"z":{"type":"string"}},"type":"object"},"name":"lookup"},"type":"function"}],"messages":[{"content":"hi","role":"user"}],"model":"gpt-4o-mini"}`),
	}

	encoded := make([][]byte, len(bodies))
	for i, body := range bodies {
		var req ChatRequest
		err := json.Unmarshal(body, &req)
		require.NoError(t, err)

		encoded[i], err = json.Marshal(req)
		require.NoError(t, err)
	}
	require.Equal(t, encoded[1], encoded[0])
}

func lookupUnknownField(t *testing.T, fields UnknownJSONFields, key string) json.RawMessage {
	t.Helper()
	raw := fields.Lookup(key)
	require.NotNil(t, raw, "unknown field %q missing", key)
	return raw
}

func TestChatRequestJSON_RoundTripPreservesUnknownFields(t *testing.T) {
	body := []byte(`{
		"model":"gpt-4o-mini",
		"messages":[
			{
				"role":"user",
				"content":"hello",
				"x_message_meta":{"id":"msg-1"},
				"tool_calls":[
					{
						"id":"call_1",
						"type":"function",
						"x_tool_call":true,
						"function":{
							"name":"lookup_weather",
							"arguments":"{}",
							"x_function_meta":{"strict":true}
						}
					}
				]
			}
		],
		"tools":[
			{
				"type":"function",
				"function":{"name":"lookup_weather","parameters":{"type":"object"}},
				"x_tool_meta":"keep-me"
			}
		],
		"stream":true,
		"x_trace":{"id":"trace-1"}
	}`)

	wantExtra, err := extractUnknownJSONFields(body,
		"temperature",
		"max_tokens",
		"model",
		"provider",
		"messages",
		"tools",
		"tool_choice",
		"parallel_tool_calls",
		"stream",
		"stream_options",
		"reasoning",
	)
	require.NoError(t, err)

	var req ChatRequest
	err = json.Unmarshal(body, &req)
	require.NoError(t, err)
	require.Equal(t, "gpt-4o-mini", req.Model)

	traceField := lookupUnknownField(t, req.ExtraFields, "x_trace")
	require.Equal(t, string(wantExtra.Lookup("x_trace")), string(traceField))

	var topTrace map[string]any
	err = json.Unmarshal(traceField, &topTrace)
	require.NoError(t, err)
	require.Equal(t, "trace-1", topTrace["id"])
	require.Len(t, req.Messages, 1)

	var messageMeta map[string]any
	err = json.Unmarshal(lookupUnknownField(t, req.Messages[0].ExtraFields, "x_message_meta"), &messageMeta)
	require.NoError(t, err)
	require.Equal(t, "msg-1", messageMeta["id"])
	require.Len(t, req.Messages[0].ToolCalls, 1)
	got := lookupUnknownField(t, req.Messages[0].ToolCalls[0].ExtraFields, "x_tool_call")
	require.Equal(t, "true", string(got))

	var functionMeta map[string]any
	err = json.Unmarshal(lookupUnknownField(t, req.Messages[0].ToolCalls[0].Function.ExtraFields, "x_function_meta"), &functionMeta)
	require.NoError(t, err)
	require.Equal(t, true, functionMeta["strict"])
	require.Equal(t, "keep-me", req.Tools[0]["x_tool_meta"])

	roundTrip, err := json.Marshal(req)
	require.NoError(t, err)

	var decoded map[string]any
	err = json.Unmarshal(roundTrip, &decoded)
	require.NoError(t, err)

	traceMap, ok := decoded["x_trace"].(map[string]any)
	require.True(t, ok, "x_trace = %#v, want object", decoded["x_trace"])
	require.Equal(t, "trace-1", traceMap["id"])

	messages, ok := decoded["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 1)

	message := messages[0].(map[string]any)
	messageMetaMap, ok := message["x_message_meta"].(map[string]any)
	require.True(t, ok, "x_message_meta = %#v, want object", message["x_message_meta"])
	require.Equal(t, "msg-1", messageMetaMap["id"])

	toolCalls := message["tool_calls"].([]any)
	toolCall := toolCalls[0].(map[string]any)
	require.Equal(t, true, toolCall["x_tool_call"])

	function := toolCall["function"].(map[string]any)
	functionMetaMap, ok := function["x_function_meta"].(map[string]any)
	require.True(t, ok, "x_function_meta = %#v, want object", function["x_function_meta"])
	require.Equal(t, true, functionMetaMap["strict"])

	tools := decoded["tools"].([]any)
	tool := tools[0].(map[string]any)
	require.Equal(t, "keep-me", tool["x_tool_meta"])
}
