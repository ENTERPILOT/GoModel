package core

import (
	"encoding/json"
	"testing"
)

func lookupUnknownField(t *testing.T, fields UnknownJSONFields, key string) json.RawMessage {
	t.Helper()
	raw := fields.Lookup(key)
	if raw == nil {
		t.Fatalf("unknown field %q missing", key)
	}
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
	if err != nil {
		t.Fatalf("extractUnknownJSONFields() error = %v", err)
	}

	var req ChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if req.Model != "gpt-4o-mini" {
		t.Fatalf("Model = %q, want gpt-4o-mini", req.Model)
	}
	traceField := lookupUnknownField(t, req.ExtraFields, "x_trace")
	if string(traceField) != string(wantExtra.Lookup("x_trace")) {
		t.Fatalf("ExtraFields[x_trace] = %s, want %s", traceField, wantExtra.Lookup("x_trace"))
	}
	var topTrace map[string]any
	if err := json.Unmarshal(traceField, &topTrace); err != nil {
		t.Fatalf("failed to unmarshal x_trace: %v", err)
	}
	if topTrace["id"] != "trace-1" {
		t.Fatalf("x_trace.id = %#v, want trace-1", topTrace["id"])
	}
	if len(req.Messages) != 1 {
		t.Fatalf("len(Messages) = %d, want 1", len(req.Messages))
	}
	var messageMeta map[string]any
	if err := json.Unmarshal(lookupUnknownField(t, req.Messages[0].ExtraFields, "x_message_meta"), &messageMeta); err != nil {
		t.Fatalf("failed to unmarshal x_message_meta: %v", err)
	}
	if messageMeta["id"] != "msg-1" {
		t.Fatalf("x_message_meta.id = %#v, want msg-1", messageMeta["id"])
	}
	if len(req.Messages[0].ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(req.Messages[0].ToolCalls))
	}
	if got := lookupUnknownField(t, req.Messages[0].ToolCalls[0].ExtraFields, "x_tool_call"); string(got) != "true" {
		t.Fatalf("x_tool_call = %s, want true", got)
	}
	var functionMeta map[string]any
	if err := json.Unmarshal(lookupUnknownField(t, req.Messages[0].ToolCalls[0].Function.ExtraFields, "x_function_meta"), &functionMeta); err != nil {
		t.Fatalf("failed to unmarshal x_function_meta: %v", err)
	}
	if functionMeta["strict"] != true {
		t.Fatalf("x_function_meta.strict = %#v, want true", functionMeta["strict"])
	}
	if got := req.Tools[0]["x_tool_meta"]; got != "keep-me" {
		t.Fatalf("tools[0][x_tool_meta] = %#v, want keep-me", got)
	}

	roundTrip, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(roundTrip, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(roundTrip) error = %v", err)
	}
	traceMap, ok := decoded["x_trace"].(map[string]any)
	if !ok {
		t.Fatalf("x_trace = %#v, want object", decoded["x_trace"])
	}
	if traceMap["id"] != "trace-1" {
		t.Fatalf("x_trace.id = %#v, want trace-1", traceMap["id"])
	}

	messages, ok := decoded["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("messages = %#v, want len=1", decoded["messages"])
	}
	message := messages[0].(map[string]any)
	messageMetaMap, ok := message["x_message_meta"].(map[string]any)
	if !ok {
		t.Fatalf("x_message_meta = %#v, want object", message["x_message_meta"])
	}
	if messageMetaMap["id"] != "msg-1" {
		t.Fatalf("x_message_meta.id = %#v, want msg-1", messageMetaMap["id"])
	}
	toolCalls := message["tool_calls"].([]any)
	toolCall := toolCalls[0].(map[string]any)
	if toolCall["x_tool_call"] != true {
		t.Fatalf("x_tool_call = %#v, want true", toolCall["x_tool_call"])
	}
	function := toolCall["function"].(map[string]any)
	functionMetaMap, ok := function["x_function_meta"].(map[string]any)
	if !ok {
		t.Fatalf("x_function_meta = %#v, want object", function["x_function_meta"])
	}
	if functionMetaMap["strict"] != true {
		t.Fatalf("x_function_meta.strict = %#v, want true", functionMetaMap["strict"])
	}

	tools := decoded["tools"].([]any)
	tool := tools[0].(map[string]any)
	if tool["x_tool_meta"] != "keep-me" {
		t.Fatalf("x_tool_meta = %#v, want keep-me", tool["x_tool_meta"])
	}
}
