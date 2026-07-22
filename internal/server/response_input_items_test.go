package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestNormalizedResponseInputItemsSkipsNilDefaultInput(t *testing.T) {
	var input *core.ResponsesInputElement
	req := &core.ResponsesRequest{Input: input}

	items := normalizedResponseInputItems("resp_1", req)
	if len(items) != 0 {
		t.Fatalf("len(items) = %d, want 0", len(items))
	}
}

func TestNormalizedResponseInputRawPreservesLargeUnknownIntegers(t *testing.T) {
	item := normalizedResponseInputRaw("resp_1", 0, json.RawMessage(
		`{"type":"future_item","opaque_integer":9007199254740993,"nested":{"value":9007199254740995}}`,
	))
	for _, want := range []string{
		`"opaque_integer":9007199254740993`,
		`"value":9007199254740995`,
	} {
		if !strings.Contains(string(item), want) {
			t.Fatalf("normalized item = %s, want %s", item, want)
		}
	}
	if responseInputItemID(item) == "" {
		t.Fatalf("normalized item = %s, want generated id", item)
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
