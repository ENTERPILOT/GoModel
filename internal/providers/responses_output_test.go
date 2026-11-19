package providers

import (
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	require.Len(t, items, 1)
	require.Equal(t, "function_call", items[0].Type)

	encoded, err := json.Marshal(items[0])
	require.NoError(t, err)

	var wire map[string]json.RawMessage
	err = json.Unmarshal(encoded, &wire)
	require.NoError(t, err)
	got := string(wire["extra_content"])
	require.Equal(t, `{"google":{"thought_signature":"sig-1"}}`, got)
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
	require.NoError(t, err)

	var wire map[string]json.RawMessage
	err = json.Unmarshal(encoded, &wire)
	require.NoError(t, err)
	_, present := wire["provider_index"]
	require.False(t, present, "provider_index leaked onto the function_call item: %s", encoded)
	got := string(wire["extra_content"])
	require.Equal(t, `{"google":{"thought_signature":"sig-1"}}`, got)
}

// The mistral provider keeps content chunks it cannot map to standard chat
// output (citation references) in a vendor member of the message. The message
// item carries them at extra_content.mistral.content_chunks, the one extension
// member the router strips from replayed items, and no other unknown member.
func TestBuildResponsesOutputItems_KeepsMistralContentChunks(t *testing.T) {
	const chunks = `[{"type":"reference","reference_ids":[0,2]}]`
	tests := []struct {
		name   string
		fields map[string]json.RawMessage
		want   string
	}{
		{
			name: "chunks ride in the message item's extra_content",
			fields: map[string]json.RawMessage{
				MistralContentChunksField: json.RawMessage(chunks),
				"provider_index":          json.RawMessage(`7`),
			},
			want: `{"mistral":{"content_chunks":` + chunks + `}}`,
		},
		{name: "null member is dropped", fields: map[string]json.RawMessage{MistralContentChunksField: json.RawMessage(`null`)}},
		{name: "no member", fields: map[string]json.RawMessage{"provider_index": json.RawMessage(`7`)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items := BuildResponsesOutputItems(core.ResponseMessage{
				Role:        "assistant",
				Content:     "Paris.",
				ExtraFields: core.UnknownJSONFieldsFromMap(tt.fields),
			})
			require.Len(t, items, 1)
			require.Equal(t, "message", items[0].Type)

			encoded, err := json.Marshal(items[0])
			require.NoError(t, err)
			var wire map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(encoded, &wire))
			assert.NotContains(t, wire, "provider_index")
			assert.NotContains(t, wire, MistralContentChunksField)
			assert.JSONEq(t, `[{"type":"output_text","text":"Paris.","annotations":[]}]`, string(wire["content"]))
			if tt.want == "" {
				assert.NotContains(t, wire, core.ExtraContentField)
				return
			}
			assert.JSONEq(t, tt.want, string(wire[core.ExtraContentField]))
		})
	}
}

// A tool-call turn without text has no message item of its own. Content
// chunks open one, so they are not lost with it.
func TestBuildResponsesOutputItems_MistralContentChunksWithoutText(t *testing.T) {
	toolCall := core.ToolCall{ID: "call_1", Type: "function", Function: core.FunctionCall{Name: "lookup", Arguments: "{}"}}
	items := BuildResponsesOutputItems(core.ResponseMessage{Role: "assistant", ToolCalls: []core.ToolCall{toolCall}})
	require.Len(t, items, 1)
	require.Equal(t, "function_call", items[0].Type)

	items = BuildResponsesOutputItems(core.ResponseMessage{
		Role:      "assistant",
		ToolCalls: []core.ToolCall{toolCall},
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			MistralContentChunksField: json.RawMessage(`[{"type":"reference","reference_ids":[9007199254740993]}]`),
		}),
	})
	require.Len(t, items, 2)
	require.Equal(t, "message", items[0].Type)
	require.Equal(t, "function_call", items[1].Type)
	assert.JSONEq(t, `{"mistral":{"content_chunks":[{"type":"reference","reference_ids":[9007199254740993]}]}}`,
		string(items[0].ExtraFields.Lookup(core.ExtraContentField)))
	assert.Contains(t, string(items[0].ExtraFields.Lookup(core.ExtraContentField)), "9007199254740993")
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
	require.NoError(t, err)

	for _, msg := range messages {
		if msg.Role != "assistant" {
			continue
		}
		raw := msg.ExtraFields.Lookup(core.ExtraContentField)
		assert.Empty(t, raw, "assistant turn %q inherited stale replay state: %s", msg.Content, raw)
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
	require.NoError(t, err)

	assistant := messages[len(messages)-1]
	require.Equal(t, "assistant", assistant.Role)
	raw := assistant.ExtraFields.Lookup(core.ExtraContentField)
	require.NotEmpty(t, raw)
}

// OpenAI never emits a message item whose only content is an empty text
// part. A tool-call-only turn arrives from some providers with content "",
// and that must not become an empty output_text block ahead of the calls.
func TestBuildResponsesOutputItems_ToolCallOnlyHasNoEmptyMessage(t *testing.T) {
	items := BuildResponsesOutputItems(core.ResponseMessage{
		Role:    "assistant",
		Content: "",
		ToolCalls: []core.ToolCall{{
			ID:       "call_1",
			Type:     "function",
			Function: core.FunctionCall{Name: "lookup_weather", Arguments: `{"city":"Warsaw"}`},
		}},
	})
	require.Len(t, items, 1)
	require.Equal(t, "function_call", items[0].Type)

	// A turn with neither text nor tool calls still yields one message so the
	// output is never empty.
	items = BuildResponsesOutputItems(core.ResponseMessage{Role: "assistant", Content: ""})
	require.Len(t, items, 1)
	require.Equal(t, "message", items[0].Type)
}

// Turn-wide replay state on the assistant message (a Gemini 3 text-turn
// thought signature, an Anthropic thinking signature) has no home on the
// message item, so it rides on a reasoning item even when there is no
// reasoning text to show. The client echoes the item and the state comes back
// on the assistant turn.
func TestBuildResponsesOutputItems_MessageReplayStateBecomesReasoningItem(t *testing.T) {
	fields, err := core.UnknownJSONFields{}.WithExtraContent(core.ExtraContentVendorGoogle, json.RawMessage(`{"thought_signature":"sig-1"}`))
	require.NoError(t, err)

	items := BuildResponsesOutputItems(core.ResponseMessage{Role: "assistant", Content: "hi", ExtraFields: fields})
	require.Len(t, items, 2)
	require.Equal(t, "reasoning", items[0].Type)
	require.Equal(t, "message", items[1].Type)

	encoded, err := json.Marshal(items[0])
	require.NoError(t, err)

	var reasoning map[string]json.RawMessage
	err = json.Unmarshal(encoded, &reasoning)
	require.NoError(t, err)
	got := string(reasoning["extra_content"])
	assert.Equal(t, `{"google":{"thought_signature":"sig-1"}}`, got)
	_, ok := reasoning["content"]
	assert.False(t, ok, "reasoning item carries content %s, want none for a turn without reasoning text", reasoning["content"])

	// Echoed back, the item's state lands on the assistant turn it precedes.
	messages, err := convertResponsesInputItems([]any{
		map[string]any{"type": "reasoning", "summary": []any{}, "extra_content": map[string]any{"google": map[string]any{"thought_signature": "sig-1"}}},
		map[string]any{"type": "message", "role": "assistant", "content": "hi"},
	})
	require.NoError(t, err)
	require.Len(t, messages, 1)
	got = string(messages[0].ExtraFields.ExtraContent(core.ExtraContentVendorGoogle))
	assert.Equal(t, `{"thought_signature":"sig-1"}`, got)
}
