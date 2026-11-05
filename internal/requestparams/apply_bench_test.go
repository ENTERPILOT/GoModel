package requestparams

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

func benchChatRequest() *core.ChatRequest {
	content := strings.Repeat("lorem ipsum dolor sit amet ", 2400) // ~64 KiB
	return &core.ChatRequest{
		Model:    "qwen-thinking-low",
		Messages: []core.Message{{Role: "user", Content: content}},
		Tools:    []map[string]any{{"type": "function", "function": map[string]any{"name": "lookup", "parameters": map[string]any{"type": "object"}}}},
	}
}

func benchResponsesRequest() *core.ResponsesRequest {
	content := strings.Repeat("lorem ipsum dolor sit amet ", 2400)
	return &core.ResponsesRequest{
		Model: "qwen-thinking-low",
		Input: []core.ResponsesInputElement{{Role: "user", Content: content}},
		Tools: []map[string]any{{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object"}}},
	}
}

var benchSampling = Set{
	"temperature":        json.RawMessage(`1`),
	"top_p":              json.RawMessage(`0.95`),
	"top_k":              json.RawMessage(`20`),
	"min_p":              json.RawMessage(`0`),
	"presence_penalty":   json.RawMessage(`0`),
	"repetition_penalty": json.RawMessage(`1`),
}

var benchForced = Set{"chat_template_kwargs": json.RawMessage(`{"enable_thinking":true,"preserve_thinking":true}`)}

func BenchmarkApplyChatSampling(b *testing.B) {
	req := benchChatRequest()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ApplyChat(req, benchSampling, benchForced); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkApplyResponsesSampling(b *testing.B) {
	req := benchResponsesRequest()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ApplyResponses(req, benchSampling, benchForced); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkApplyResponsesTypedMerge(b *testing.B) {
	req := benchResponsesRequest()
	params := Set{"metadata": json.RawMessage(`{"alias":"qwen-thinking-low"}`)}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ApplyResponses(req, nil, params); err != nil {
			b.Fatal(err)
		}
	}
}
