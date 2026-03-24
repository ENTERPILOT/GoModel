package core

import (
	"encoding/json"
	"testing"
)

var benchmarkChatUnknownFieldsPayload = []byte(`{
	"model":"gpt-5-mini",
	"messages":[
		{
			"role":"user",
			"name":"alice",
			"content":[
				{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}},
				{"type":"image_url","image_url":{"url":"https://example.com/a.png","detail":"high","x_nested":"image-extra"}}
			],
			"x_message_meta":{"id":"msg-1"}
		},
		{
			"role":"assistant",
			"content":null,
			"tool_calls":[
				{
					"id":"call_123",
					"type":"function",
					"x_tool_call":true,
					"function":{"name":"lookup_weather","arguments":"{}","x_function_meta":{"strict":true}}
				}
			]
		}
	],
	"tools":[{"type":"function","function":{"name":"lookup_weather","parameters":{"type":"object"}},"x_tool_meta":"keep-me"}],
	"response_format":{"type":"json_schema","json_schema":{"name":"math_response"}},
	"stream":true,
	"x_trace":{"id":"trace-1"}
}`)

var benchmarkResponsesUnknownFieldsPayload = []byte(`{
	"model":"gpt-5-mini",
	"input":[
		{"type":"message","id":"msg_123","role":"user","content":"hello","x_trace":{"id":"trace-1"}},
		{"type":"function_call","call_id":"call_123","name":"lookup_weather","arguments":"{}","strict":true},
		{"type":"function_call_output","call_id":"call_123","name":"still-extra","output":{"temperature_c":21}}
	],
	"text":{"format":{"type":"json_schema","name":"answer"}},
	"metadata":{"tenant":"acme"}
}`)

func BenchmarkExtractUnknownJSONFieldsMap_Chat(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		fields, err := extractUnknownJSONFieldsMapBaseline(benchmarkChatUnknownFieldsPayload,
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
			b.Fatal(err)
		}
		if len(fields) == 0 {
			b.Fatal("expected unknown fields")
		}
	}
}

func BenchmarkExtractUnknownJSONFieldsObject_Chat(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		fields, err := extractUnknownJSONFields(benchmarkChatUnknownFieldsPayload,
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
			b.Fatal(err)
		}
		if fields.IsEmpty() {
			b.Fatal("expected unknown fields")
		}
	}
}

func BenchmarkExtractUnknownJSONFieldsMap_Responses(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		fields, err := extractUnknownJSONFieldsMapBaseline(benchmarkResponsesUnknownFieldsPayload,
			"model",
			"provider",
			"input",
			"instructions",
			"tools",
			"tool_choice",
			"parallel_tool_calls",
			"temperature",
			"max_output_tokens",
			"stream",
			"stream_options",
			"metadata",
			"reasoning",
		)
		if err != nil {
			b.Fatal(err)
		}
		if len(fields) == 0 {
			b.Fatal("expected unknown fields")
		}
	}
}

func BenchmarkExtractUnknownJSONFieldsObject_Responses(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		fields, err := extractUnknownJSONFields(benchmarkResponsesUnknownFieldsPayload,
			"model",
			"provider",
			"input",
			"instructions",
			"tools",
			"tool_choice",
			"parallel_tool_calls",
			"temperature",
			"max_output_tokens",
			"stream",
			"stream_options",
			"metadata",
			"reasoning",
		)
		if err != nil {
			b.Fatal(err)
		}
		if fields.IsEmpty() {
			b.Fatal("expected unknown fields")
		}
	}
}

func BenchmarkMarshalUnknownJSONFieldsMap_Chat(b *testing.B) {
	b.ReportAllocs()
	extraFields, err := extractUnknownJSONFieldsMapBaseline(benchmarkChatUnknownFieldsPayload,
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
		b.Fatal(err)
	}
	base := struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream,omitempty"`
	}{
		Model:  "gpt-5-mini",
		Stream: true,
	}
	for b.Loop() {
		body, err := marshalWithUnknownJSONFieldsMapBaseline(base, extraFields)
		if err != nil {
			b.Fatal(err)
		}
		if len(body) == 0 {
			b.Fatal("expected output")
		}
	}
}

func BenchmarkMarshalUnknownJSONFieldsObject_Chat(b *testing.B) {
	b.ReportAllocs()
	extraFields, err := extractUnknownJSONFields(benchmarkChatUnknownFieldsPayload,
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
		b.Fatal(err)
	}
	base := struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream,omitempty"`
	}{
		Model:  "gpt-5-mini",
		Stream: true,
	}
	for b.Loop() {
		body, err := marshalWithUnknownJSONFields(base, extraFields)
		if err != nil {
			b.Fatal(err)
		}
		if len(body) == 0 {
			b.Fatal("expected output")
		}
	}
}

func extractUnknownJSONFieldsMapBaseline(data []byte, knownFields ...string) (map[string]json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	for _, field := range knownFields {
		delete(raw, field)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	cloned := make(map[string]json.RawMessage, len(raw))
	for key, value := range raw {
		cloned[key] = CloneRawJSON(value)
	}
	return cloned, nil
}

func marshalWithUnknownJSONFieldsMapBaseline(base any, extraFields map[string]json.RawMessage) ([]byte, error) {
	baseBody, err := json.Marshal(base)
	if err != nil {
		return nil, err
	}
	if len(extraFields) == 0 {
		return baseBody, nil
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(baseBody, &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		raw = make(map[string]json.RawMessage)
	}
	for key, value := range extraFields {
		if _, exists := raw[key]; exists {
			continue
		}
		raw[key] = CloneRawJSON(value)
	}
	return json.Marshal(raw)
}
