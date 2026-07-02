package providers

import (
	"strings"
	"testing"

	"github.com/goccy/go-json"
)

func decodeChunkSSE(t *testing.T, line string) map[string]any {
	t.Helper()
	payload, ok := strings.CutPrefix(line, "data: ")
	if !ok || !strings.HasSuffix(payload, "\n\n") {
		t.Fatalf("chunk = %q, want data: <json>\\n\\n framing", line)
	}
	var chunk map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSuffix(payload, "\n\n")), &chunk); err != nil {
		t.Fatalf("unmarshal chunk payload: %v", err)
	}
	return chunk
}

func TestFormatChatChunkSSE(t *testing.T) {
	chunk := decodeChunkSSE(t, FormatChatChunkSSE(
		"chunk-1", 1700000000, "claude-3", "anthropic",
		map[string]any{"content": "hi"}, nil, nil,
	))

	if got := chunk["id"]; got != "chunk-1" {
		t.Fatalf("id = %v, want chunk-1", got)
	}
	if got := chunk["object"]; got != "chat.completion.chunk" {
		t.Fatalf("object = %v, want chat.completion.chunk", got)
	}
	if got := chunk["created"]; got != float64(1700000000) {
		t.Fatalf("created = %v, want 1700000000", got)
	}
	if got := chunk["model"]; got != "claude-3" {
		t.Fatalf("model = %v, want claude-3", got)
	}
	if got := chunk["provider"]; got != "anthropic" {
		t.Fatalf("provider = %v, want anthropic", got)
	}
	if _, present := chunk["usage"]; present {
		t.Fatal("usage present, want omitted when nil")
	}

	choices, ok := chunk["choices"].([]any)
	if !ok || len(choices) != 1 {
		t.Fatalf("choices = %#v, want exactly one choice", chunk["choices"])
	}
	choice := choices[0].(map[string]any)
	if got := choice["index"]; got != float64(0) {
		t.Fatalf("choice index = %v, want 0", got)
	}
	if choice["finish_reason"] != nil {
		t.Fatalf("finish_reason = %v, want explicit null", choice["finish_reason"])
	}
	delta, _ := choice["delta"].(map[string]any)
	if got := delta["content"]; got != "hi" {
		t.Fatalf("delta content = %v, want hi", got)
	}
}

func TestFormatChatChunkSSEWithFinishReasonAndUsage(t *testing.T) {
	chunk := decodeChunkSSE(t, FormatChatChunkSSE(
		"chunk-2", 1700000000, "claude-3", "anthropic",
		map[string]any{}, "stop",
		map[string]any{"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7},
	))

	choice := chunk["choices"].([]any)[0].(map[string]any)
	if got := choice["finish_reason"]; got != "stop" {
		t.Fatalf("finish_reason = %v, want stop", got)
	}
	usage, ok := chunk["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage = %#v, want object", chunk["usage"])
	}
	if got := usage["total_tokens"]; got != float64(7) {
		t.Fatalf("usage total_tokens = %v, want 7", got)
	}
}
