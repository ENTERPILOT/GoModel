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

func TestBuildResponsesOutputItems_ForwardsOnlyExtraContent(t *testing.T) {
	items := BuildResponsesOutputItems(core.ResponseMessage{
		Role: "assistant",
		ToolCalls: []core.ToolCall{{
			ID:       "call_1",
			Type:     "function",
			Function: core.FunctionCall{Name: "lookup_weather", Arguments: "{}"},
			ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
				"extra_content":  json.RawMessage(`{"google":{"thought_signature":"sig-1"}}`),
				"provider_index": json.RawMessage(`7`),
			}),
		}},
	})
	encoded, err := json.Marshal(items[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := wire["provider_index"]; present {
		t.Fatalf("provider_index leaked onto the function_call item: %s", encoded)
	}
	if got := string(wire["extra_content"]); got != `{"google":{"thought_signature":"sig-1"}}` {
		t.Fatalf("extra_content = %s, want thought signature", got)
	}
}

// A reasoning item belongs to the assistant turn that follows it. When no
// assistant turn does — the history moves straight on to a user message — the
// replay state must be dropped, not held over and pinned to whatever assistant
// turn comes later: replaying another turn's thinking blocks is what Anthropic
// rejects.
func TestConvertResponsesInputToMessages_ReasoningDoesNotLeakForward(t *testing.T) {
	input := []any{
		map[string]any{"type": "message", "role": "user", "content": "first"},
		map[string]any{
			"type":          "reasoning",
			"content":       []any{map[string]any{"type": "reasoning_text", "text": "thinking about the first"}},
			"extra_content": map[string]any{"anthropic": map[string]any{"thinking_blocks": []any{map[string]any{"type": "thinking", "thinking": "first", "signature": "sig-first"}}}},
		},
		map[string]any{"type": "message", "role": "user", "content": "never mind, something else"},
		map[string]any{"type": "message", "role": "assistant", "content": "unrelated answer"},
	}

	messages, err := ConvertResponsesInputToMessages(input)
	if err != nil {
		t.Fatalf("ConvertResponsesInputToMessages: %v", err)
	}
	for _, msg := range messages {
		if msg.Role != "assistant" {
			continue
		}
		if raw := msg.ExtraFields.Lookup(core.ExtraContentField); len(raw) > 0 {
			t.Errorf("assistant turn %q inherited stale replay state: %s", msg.Content, raw)
		}
	}
}

// The ordinary case still attaches: a reasoning item immediately followed by
// its assistant turn hands the replay state over.
func TestConvertResponsesInputToMessages_ReasoningAttachesToItsTurn(t *testing.T) {
	const replay = `{"anthropic":{"thinking_blocks":[{"type":"thinking","thinking":"hm","signature":"sig-1"}]}}`
	input := []any{
		map[string]any{"type": "message", "role": "user", "content": "question"},
		map[string]any{
			"type":          "reasoning",
			"content":       []any{map[string]any{"type": "reasoning_text", "text": "hm"}},
			"extra_content": json.RawMessage(replay),
		},
		map[string]any{"type": "message", "role": "assistant", "content": "answer"},
	}

	messages, err := ConvertResponsesInputToMessages(input)
	if err != nil {
		t.Fatalf("ConvertResponsesInputToMessages: %v", err)
	}
	assistant := messages[len(messages)-1]
	if assistant.Role != "assistant" {
		t.Fatalf("last message role = %q, want assistant", assistant.Role)
	}
	if raw := assistant.ExtraFields.Lookup(core.ExtraContentField); len(raw) == 0 {
		t.Fatal("the assistant turn lost its replay state")
	}
}

// Turn-wide replay state on the assistant message (a Gemini 3 text-turn
// thought signature, an Anthropic thinking signature) has no home on the
// message item, so it rides on a reasoning item even when there is no
// reasoning text to show. The client echoes the item and the state comes back
// on the assistant turn.
func TestBuildResponsesOutputItems_MessageReplayStateBecomesReasoningItem(t *testing.T) {
	fields, err := core.UnknownJSONFields{}.WithExtraContent(core.ExtraContentVendorGoogle, json.RawMessage(`{"thought_signature":"sig-1"}`))
	if err != nil {
		t.Fatalf("WithExtraContent: %v", err)
	}
	items := BuildResponsesOutputItems(core.ResponseMessage{Role: "assistant", Content: "hi", ExtraFields: fields})
	if len(items) != 2 || items[0].Type != "reasoning" || items[1].Type != "message" {
		t.Fatalf("items = %+v, want a reasoning item followed by the message", items)
	}
	encoded, err := json.Marshal(items[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var reasoning map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &reasoning); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := string(reasoning["extra_content"]); got != `{"google":{"thought_signature":"sig-1"}}` {
		t.Errorf("reasoning extra_content = %s, want the message's replay state", got)
	}
	if _, ok := reasoning["content"]; ok {
		t.Errorf("reasoning item carries content %s, want none for a turn without reasoning text", reasoning["content"])
	}

	// Echoed back, the item's state lands on the assistant turn it precedes.
	messages, err := convertResponsesInputItems([]any{
		map[string]any{"type": "reasoning", "summary": []any{}, "extra_content": map[string]any{"google": map[string]any{"thought_signature": "sig-1"}}},
		map[string]any{"type": "message", "role": "assistant", "content": "hi"},
	})
	if err != nil {
		t.Fatalf("convertResponsesInputItems: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages = %+v, want one assistant turn", messages)
	}
	if got := string(messages[0].ExtraFields.ExtraContent(core.ExtraContentVendorGoogle)); got != `{"thought_signature":"sig-1"}` {
		t.Errorf("assistant extra_content.google = %s, want the echoed signature", got)
	}
}
