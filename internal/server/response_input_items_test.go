package server

import (
	"encoding/json"
	"testing"

	"gomodel/internal/core"
)

func TestNormalizedResponseInputItemsSkipsNilDefaultInput(t *testing.T) {
	var input *core.ResponsesInputElement
	req := &core.ResponsesRequest{Input: input}

	items := normalizedResponseInputItems("resp_1", req)
	if len(items) != 0 {
		t.Fatalf("len(items) = %d, want 0", len(items))
	}
}

func TestNormalizedResponseInputRawSkipsNullObject(t *testing.T) {
	item := normalizedResponseInputRaw("resp_1", 0, json.RawMessage("null"))
	if len(item) != 0 {
		t.Fatalf("len(item) = %d, want 0", len(item))
	}
}

func TestNormalizedResponseInputRawDecodesJSONStringFallback(t *testing.T) {
	item := normalizedResponseInputRaw("resp_1", 0, json.RawMessage(`"hello"`))
	if len(item) == 0 {
		t.Fatal("item is empty")
	}

	var decoded map[string]any
	if err := json.Unmarshal(item, &decoded); err != nil {
		t.Fatalf("decode item: %v", err)
	}
	content, ok := decoded["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("content = %+v, want one item", decoded["content"])
	}
	first, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("content[0] = %T, want object", content[0])
	}
	if first["text"] != "hello" {
		t.Fatalf("text = %q, want hello", first["text"])
	}
}
