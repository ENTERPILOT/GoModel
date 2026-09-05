package providers

import (
	"testing"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestBuildResponsesOutputItems_KeepsToolCallExtraFields(t *testing.T) {
	extra := core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
		"extra_content": json.RawMessage(`{"google":{"thought_signature":"sig-1"}}`),
	})
	items := BuildResponsesOutputItems(core.ResponseMessage{
		Role: "assistant",
		ToolCalls: []core.ToolCall{{
			ID:          "call_1",
			Type:        "function",
			Function:    core.FunctionCall{Name: "lookup_weather", Arguments: `{"city":"Warsaw"}`},
			ExtraFields: extra,
		}},
	})
	if len(items) != 1 || items[0].Type != "function_call" {
		t.Fatalf("items = %+v, want one function_call", items)
	}
	encoded, err := json.Marshal(items[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := string(wire["extra_content"]); got != `{"google":{"thought_signature":"sig-1"}}` {
		t.Fatalf("extra_content = %s, want thought signature", got)
	}
}
