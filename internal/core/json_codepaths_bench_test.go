package core

import (
	"encoding/json"
	"testing"
)

var benchmarkChatRequestPayload = []byte(`{
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

var benchmarkResponsesRequestPayload = []byte(`{
	"model":"gpt-5-mini",
	"input":[
		{"type":"message","id":"msg_123","role":"user","content":"hello","x_trace":{"id":"trace-1"}},
		{"type":"function_call","call_id":"call_123","name":"lookup_weather","arguments":"{}","strict":true},
		{"type":"function_call_output","call_id":"call_123","name":"still-extra","output":{"temperature_c":21}}
	],
	"text":{"format":{"type":"json_schema","name":"answer"}},
	"metadata":{"tenant":"acme"}
}`)

func BenchmarkChatRequestJSONUnmarshal(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		var req ChatRequest
		if err := json.Unmarshal(benchmarkChatRequestPayload, &req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkChatRequestJSONMarshal(b *testing.B) {
	b.ReportAllocs()
	var req ChatRequest
	if err := json.Unmarshal(benchmarkChatRequestPayload, &req); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		body, err := json.Marshal(req)
		if err != nil {
			b.Fatal(err)
		}
		if len(body) == 0 {
			b.Fatal("expected output")
		}
	}
}

func BenchmarkResponsesRequestJSONUnmarshal(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		var req ResponsesRequest
		if err := json.Unmarshal(benchmarkResponsesRequestPayload, &req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkResponsesRequestJSONMarshal(b *testing.B) {
	b.ReportAllocs()
	var req ResponsesRequest
	if err := json.Unmarshal(benchmarkResponsesRequestPayload, &req); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		body, err := json.Marshal(req)
		if err != nil {
			b.Fatal(err)
		}
		if len(body) == 0 {
			b.Fatal("expected output")
		}
	}
}
