package providers

import (
	"net/http"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func responsesInputItems(t *testing.T, input any) []core.ResponsesInputElement {
	t.Helper()
	items, ok := input.([]core.ResponsesInputElement)
	require.True(t, ok, "input must be []core.ResponsesInputElement, got %T", input)
	return items
}

func TestConvertMessagesToResponsesInput_RoleMapping(t *testing.T) {
	tests := []struct {
		name             string
		messages         []core.Message
		wantInstructions string
		wantItems        []core.ResponsesInputElement
	}{
		{
			name: "system message becomes instructions",
			messages: []core.Message{
				{Role: "system", Content: "be terse"},
				{Role: "user", Content: "hi"},
			},
			wantInstructions: "be terse",
			wantItems: []core.ResponsesInputElement{
				{Type: "message", Role: "user", Content: []any{map[string]any{"type": "input_text", "text": "hi"}}},
			},
		},
		{
			name: "developer message becomes instructions",
			messages: []core.Message{
				{Role: "developer", Content: "be nice"},
			},
			wantInstructions: "be nice",
		},
		{
			name: "multiple system messages join with a blank line",
			messages: []core.Message{
				{Role: "system", Content: "one"},
				{Role: "developer", Content: "two"},
				{Role: "user", Content: "hi"},
			},
			wantInstructions: "one\n\ntwo",
			wantItems: []core.ResponsesInputElement{
				{Type: "message", Role: "user", Content: []any{map[string]any{"type": "input_text", "text": "hi"}}},
			},
		},
		{
			name: "system message with structured content joins text parts",
			messages: []core.Message{
				{Role: "system", Content: []core.ContentPart{
					{Type: "text", Text: "one"},
					{Type: "text", Text: "two"},
				}},
			},
			wantInstructions: "one two",
		},
		{
			name: "assistant message becomes output_text message item",
			messages: []core.Message{
				{Role: "assistant", Content: "hello"},
			},
			wantItems: []core.ResponsesInputElement{
				{Type: "message", Role: "assistant", Content: []any{map[string]any{"type": "output_text", "text": "hello"}}},
			},
		},
		{
			name: "tool message becomes function_call_output item",
			messages: []core.Message{
				{Role: "tool", ToolCallID: "call_1", Content: "sunny"},
			},
			wantItems: []core.ResponsesInputElement{
				{Type: "function_call_output", CallID: "call_1", Output: "sunny"},
			},
		},
		{
			name: "system message stays in place after another role",
			messages: []core.Message{
				{Role: "system", Content: "be terse"},
				{Role: "user", Content: "hi"},
				{Role: "system", Content: "now be loud"},
			},
			wantInstructions: "be terse",
			wantItems: []core.ResponsesInputElement{
				{Type: "message", Role: "user", Content: []any{map[string]any{"type": "input_text", "text": "hi"}}},
				{Type: "message", Role: "system", Content: []any{map[string]any{"type": "input_text", "text": "now be loud"}}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, instructions, err := ConvertMessagesToResponsesInput(tt.messages)
			require.NoError(t, err)
			assert.Equal(t, tt.wantInstructions, instructions)
			if len(tt.wantItems) == 0 {
				// Instruction-only rows now return a non-nil empty slice
				// (providers reject a nil input); the content stays empty.
				require.NotNil(t, input)
				assert.Empty(t, input)
				return
			}
			assert.Equal(t, tt.wantItems, responsesInputItems(t, input))
		})
	}
}

func TestConvertMessagesToResponsesInput_MultimodalParts(t *testing.T) {
	messages := []core.Message{
		{Role: "user", Content: []core.ContentPart{
			{Type: "text", Text: "what is this"},
			{Type: "image_url", ImageURL: &core.ImageURLContent{URL: "https://example.com/a.png", Detail: "low"}},
			{Type: "input_audio", InputAudio: &core.InputAudioContent{Data: "aGk=", Format: "wav"}},
			{Type: "file", File: &core.FileContent{FileURL: "https://example.com/doc.pdf", Filename: "doc.pdf"}},
			{Type: "file", File: &core.FileContent{FileID: "file-123"}},
		}},
	}

	input, instructions, err := ConvertMessagesToResponsesInput(messages)
	require.NoError(t, err)
	assert.Empty(t, instructions)

	items := responsesInputItems(t, input)
	require.Len(t, items, 1)
	item := items[0]
	assert.Equal(t, "message", item.Type)
	assert.Equal(t, "user", item.Role)

	blocks, ok := item.Content.([]any)
	require.True(t, ok, "content must be []any blocks, got %T", item.Content)
	require.Len(t, blocks, 5)

	assert.Equal(t, map[string]any{"type": "input_text", "text": "what is this"}, blocks[0])
	assert.Equal(t, map[string]any{"type": "input_image", "image_url": "https://example.com/a.png", "detail": "low"}, blocks[1])
	assert.Equal(t, map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": "aGk=", "format": "wav"}}, blocks[2])
	assert.Equal(t, map[string]any{"type": "input_file", "file_url": "https://example.com/doc.pdf", "filename": "doc.pdf"}, blocks[3])
	assert.Equal(t, map[string]any{"type": "input_file", "file_id": "file-123"}, blocks[4])

	encoded, err := json.Marshal(input)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), `"type":"text"`, "chat spelling leaked into Responses input: %s", encoded)
}

func TestConvertMessagesToResponsesInput_AssistantToolCalls(t *testing.T) {
	messages := []core.Message{
		{
			Role:    "assistant",
			Content: "let me check",
			ToolCalls: []core.ToolCall{
				{ID: "call_1", Type: "function", Function: core.FunctionCall{Name: "get_weather", Arguments: `{"city":"Oslo"}`}},
				{ID: "call_2", Type: "function", Function: core.FunctionCall{Name: "get_time", Arguments: `{}`}},
			},
		},
	}

	input, _, err := ConvertMessagesToResponsesInput(messages)
	require.NoError(t, err)

	items := responsesInputItems(t, input)
	require.Len(t, items, 3)

	// The message item comes first, then one function_call item per tool call
	// in order.
	assert.Equal(t, "message", items[0].Type)
	assert.Equal(t, "assistant", items[0].Role)
	assert.Equal(t, []any{map[string]any{"type": "output_text", "text": "let me check"}}, items[0].Content)

	assert.Equal(t, core.ResponsesInputElement{
		Type:      "function_call",
		CallID:    "call_1",
		Name:      "get_weather",
		Arguments: `{"city":"Oslo"}`,
	}, items[1])
	assert.Equal(t, core.ResponsesInputElement{
		Type:      "function_call",
		CallID:    "call_2",
		Name:      "get_time",
		Arguments: `{}`,
	}, items[2])
}

func TestConvertMessagesToResponsesInput_AssistantToolCallOnlyTurn(t *testing.T) {
	messages := []core.Message{
		{
			Role:        "assistant",
			Content:     "",
			ContentNull: true,
			ToolCalls: []core.ToolCall{
				{ID: "call_9", Type: "function", Function: core.FunctionCall{Name: "ping", Arguments: `{}`}},
			},
		},
	}

	input, _, err := ConvertMessagesToResponsesInput(messages)
	require.NoError(t, err)

	// A tool-call-only turn emits no blank message item.
	items := responsesInputItems(t, input)
	require.Len(t, items, 1)
	assert.Equal(t, "function_call", items[0].Type)
	assert.Equal(t, "call_9", items[0].CallID)
}

func TestConvertMessagesToResponsesInput_MintsCallIDWhenEmpty(t *testing.T) {
	messages := []core.Message{
		{
			Role:    "assistant",
			Content: "",
			ToolCalls: []core.ToolCall{
				{Type: "function", Function: core.FunctionCall{Name: "ping", Arguments: `{}`}},
			},
		},
	}

	input, _, err := ConvertMessagesToResponsesInput(messages)
	require.NoError(t, err)

	items := responsesInputItems(t, input)
	require.Len(t, items, 1)
	assert.True(t, strings.HasPrefix(items[0].CallID, "call_"), "minted call_id = %q", items[0].CallID)
}

func TestConvertMessagesToResponsesInput_ToolOutputStringify(t *testing.T) {
	tests := []struct {
		name       string
		content    core.MessageContent
		wantOutput string
	}{
		{name: "string passes through", content: "done", wantOutput: "done"},
		{name: "nil becomes empty", content: nil, wantOutput: ""},
		{
			name:       "structured parts stringify via JSON",
			content:    []core.ContentPart{{Type: "text", Text: "row"}},
			wantOutput: `[{"type":"text","text":"row"}]`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, _, err := ConvertMessagesToResponsesInput([]core.Message{
				{Role: "tool", ToolCallID: "call_1", Content: tt.content},
			})
			require.NoError(t, err)

			items := responsesInputItems(t, input)
			require.Len(t, items, 1)
			assert.Equal(t, "function_call_output", items[0].Type)
			assert.Equal(t, "call_1", items[0].CallID)
			assert.Equal(t, tt.wantOutput, items[0].Output)
		})
	}
}

func TestConvertMessagesToResponsesInput_ReasoningReplay(t *testing.T) {
	replayState := json.RawMessage(`{"openai":{"encrypted_content":"abc"}}`)
	messages := []core.Message{
		{
			Role:    "assistant",
			Content: "answer",
			ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
				"reasoning_content":    json.RawMessage(`"thinking hard"`),
				core.ExtraContentField: replayState,
			}),
		},
	}

	input, _, err := ConvertMessagesToResponsesInput(messages)
	require.NoError(t, err)

	items := responsesInputItems(t, input)
	require.Len(t, items, 2)

	// The reasoning item precedes the assistant message item and carries the
	// replay state plus the reasoning text.
	reasoning := items[0]
	assert.Equal(t, "reasoning", reasoning.Type)

	encoded, err := json.Marshal(reasoning)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"type": "reasoning",
		"summary": [],
		"content": [{"type": "reasoning_text", "text": "thinking hard"}],
		"extra_content": {"openai": {"encrypted_content": "abc"}}
	}`, string(encoded))

	message := items[1]
	assert.Equal(t, "message", message.Type)
	assert.Equal(t, "assistant", message.Role)
	// Consumed reasoning members must not leak onto the message item.
	assert.True(t, message.ExtraFields.IsEmpty(), "message extras = %v", message.ExtraFields)
}

func TestConvertMessagesToResponsesInput_ReasoningReplayWithoutText(t *testing.T) {
	messages := []core.Message{
		{
			Role:    "assistant",
			Content: "answer",
			ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
				core.ExtraContentField: json.RawMessage(`{"openai":{"encrypted_content":"abc"}}`),
			}),
		},
	}

	input, _, err := ConvertMessagesToResponsesInput(messages)
	require.NoError(t, err)

	// Redacted reasoning has no readable text and still replays.
	items := responsesInputItems(t, input)
	require.Len(t, items, 2)
	assert.Equal(t, "reasoning", items[0].Type)

	encoded, err := json.Marshal(items[0])
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), `"content"`)
}

func TestConvertMessagesToResponsesInput_ReasoningTextWithoutReplayStateDropped(t *testing.T) {
	messages := []core.Message{
		{
			Role:    "assistant",
			Content: "answer",
			ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
				"reasoning_content": json.RawMessage(`"thinking hard"`),
			}),
		},
	}

	input, _, err := ConvertMessagesToResponsesInput(messages)
	require.NoError(t, err)

	// Reasoning text alone is not replayable: no reasoning item is emitted.
	items := responsesInputItems(t, input)
	require.Len(t, items, 1)
	assert.Equal(t, "message", items[0].Type)
	assert.True(t, items[0].ExtraFields.IsEmpty(), "reasoning text must not leak onto the message item")
}

func TestConvertMessagesToResponsesInput_RejectsUnknownPartType(t *testing.T) {
	tests := []struct {
		name     string
		messages []core.Message
		wantName string
	}{
		{
			name: "video_url part on user message",
			messages: []core.Message{
				{Role: "user", Content: []core.ContentPart{{Type: "video_url", VideoURL: &core.VideoURLContent{URL: "https://example.com/v.mp4"}}}},
			},
			wantName: "video_url",
		},
		{
			name: "unknown part on assistant message",
			messages: []core.Message{
				{Role: "assistant", Content: []core.ContentPart{{Type: "hologram"}}},
			},
			wantName: "hologram",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, _, err := ConvertMessagesToResponsesInput(tt.messages)
			require.Error(t, err)
			assert.Nil(t, input)

			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())
			assert.Contains(t, gatewayErr.Message, tt.wantName)
		})
	}
}

func TestConvertMessagesToResponsesInput_ContentEdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		content     core.MessageContent
		wantContent any
	}{
		{name: "nil content becomes an empty string", content: nil, wantContent: ""},
		{name: "empty string content stays empty", content: "", wantContent: ""},
		{name: "empty parts array becomes an empty string", content: []any{}, wantContent: ""},
		{name: "unnormalizable parts become an empty string", content: []any{"nope"}, wantContent: ""},
		{name: "non-text scalar content becomes an empty string", content: 42, wantContent: ""},
		{
			name:        "dynamic JSON parts convert",
			content:     []any{map[string]any{"type": "text", "text": "hi"}},
			wantContent: []any{map[string]any{"type": "input_text", "text": "hi"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, _, err := ConvertMessagesToResponsesInput([]core.Message{
				{Role: "user", Content: tt.content},
			})
			require.NoError(t, err)

			items := responsesInputItems(t, input)
			require.Len(t, items, 1)
			assert.Equal(t, tt.wantContent, items[0].Content)
		})
	}
}

func TestConvertMessagesToResponsesInput_MalformedPartsSkipped(t *testing.T) {
	// Malformed parts of a known type are skipped, matching
	// buildResponsesContentItemsFromParts on the inbound path.
	messages := []core.Message{
		{Role: "user", Content: []core.ContentPart{
			{Type: "text", Text: ""},
			{Type: "image_url"},
			{Type: "image_url", ImageURL: &core.ImageURLContent{URL: "  "}},
			{Type: "input_audio"},
			{Type: "input_audio", InputAudio: &core.InputAudioContent{Data: "aGk="}},
			{Type: "file", File: &core.FileContent{}},
			{Type: "file", File: &core.FileContent{FileData: "aGk=", Filename: "a.pdf"}},
		}},
	}

	input, _, err := ConvertMessagesToResponsesInput(messages)
	require.NoError(t, err)

	items := responsesInputItems(t, input)
	require.Len(t, items, 1)
	blocks, ok := items[0].Content.([]any)
	require.True(t, ok, "content must be []any blocks, got %T", items[0].Content)
	require.Len(t, blocks, 1)
	assert.Equal(t, map[string]any{"type": "input_file", "file_data": "aGk=", "filename": "a.pdf"}, blocks[0])
}

func TestConvertMessagesToResponsesInput_ToolOutputUnserializable(t *testing.T) {
	input, _, err := ConvertMessagesToResponsesInput([]core.Message{
		{Role: "tool", ToolCallID: "call_1", Content: map[string]any{"callback": func() {}}},
	})
	require.Error(t, err)
	assert.Nil(t, input)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())
	assert.Contains(t, gatewayErr.Message, "function_call_output")
}

func TestConvertMessagesToResponsesInput_ToolMessageRequiresToolCallID(t *testing.T) {
	input, _, err := ConvertMessagesToResponsesInput([]core.Message{
		{Role: "tool", Content: "done"},
	})
	require.Error(t, err)
	assert.Nil(t, input)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())
	assert.Contains(t, gatewayErr.Message, "tool_call_id")
}

func TestConvertMessagesToResponsesInput_MessageRequiresRole(t *testing.T) {
	input, _, err := ConvertMessagesToResponsesInput([]core.Message{
		{Content: "hi"},
	})
	require.Error(t, err)
	assert.Nil(t, input)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())
	assert.Contains(t, gatewayErr.Message, "role")
}

func TestConvertMessagesToResponsesInput_RequiresInputOrInstructions(t *testing.T) {
	// An empty conversation would marshal as "input":null upstream; the
	// gateway rejects it with a 400 naming messages instead.
	for _, messages := range [][]core.Message{nil, {}} {
		input, _, err := ConvertMessagesToResponsesInput(messages)
		require.Error(t, err)
		assert.Nil(t, input)

		var gatewayErr *core.GatewayError
		require.ErrorAs(t, err, &gatewayErr)
		assert.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())
		assert.Contains(t, gatewayErr.Message, "messages")
	}
}

func TestConvertMessagesToResponsesInput_InstructionOnlyHasEmptyNonNilInput(t *testing.T) {
	// A system-only conversation yields instructions but no input items. The
	// input must stay a non-nil empty slice: providers such as chatgpt reject
	// a nil input even when instructions carry the prompt.
	input, instructions, err := ConvertMessagesToResponsesInput([]core.Message{
		{Role: "system", Content: "you are terse"},
	})
	require.NoError(t, err)
	assert.Equal(t, "you are terse", instructions)
	require.NotNil(t, input)
	assert.Empty(t, input)
}

func TestConvertMessagesToResponsesInput_StripsChatOnlyExtras(t *testing.T) {
	extras := func() core.UnknownJSONFields {
		return core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			"name":     json.RawMessage(`"alice"`),
			"refusal":  json.RawMessage(`"no"`),
			"x_vendor": json.RawMessage(`"keep-me"`),
		})
	}

	t.Run("message items drop name and refusal", func(t *testing.T) {
		input, _, err := ConvertMessagesToResponsesInput([]core.Message{
			{Role: "assistant", Content: "answer", ExtraFields: extras()},
		})
		require.NoError(t, err)

		items := responsesInputItems(t, input)
		require.Len(t, items, 1)
		assert.Empty(t, items[0].ExtraFields.Lookup("name"))
		assert.Empty(t, items[0].ExtraFields.Lookup("refusal"))
		assert.JSONEq(t, `"keep-me"`, string(items[0].ExtraFields.Lookup("x_vendor")))
	})

	t.Run("function_call_output drops name", func(t *testing.T) {
		input, _, err := ConvertMessagesToResponsesInput([]core.Message{
			{Role: "tool", ToolCallID: "call_1", Content: "done", ExtraFields: extras()},
		})
		require.NoError(t, err)

		items := responsesInputItems(t, input)
		require.Len(t, items, 1)
		assert.Empty(t, items[0].ExtraFields.Lookup("name"))
		assert.JSONEq(t, `"keep-me"`, string(items[0].ExtraFields.Lookup("x_vendor")))
	})
}

func TestConvertMessagesToResponsesInput_NormalizesEmptyToolCallArguments(t *testing.T) {
	tests := []struct {
		name      string
		arguments string
	}{
		{name: "empty", arguments: ""},
		{name: "whitespace", arguments: "  "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, _, err := ConvertMessagesToResponsesInput([]core.Message{
				{
					Role: "assistant",
					ToolCalls: []core.ToolCall{
						{ID: "call_1", Type: "function", Function: core.FunctionCall{Name: "ping", Arguments: tt.arguments}},
					},
				},
			})
			require.NoError(t, err)

			items := responsesInputItems(t, input)
			require.Len(t, items, 1)
			assert.Equal(t, `{}`, items[0].Arguments)
		})
	}
}

func TestConvertMessagesToResponsesInput_DynamicContentEdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		content     core.MessageContent
		wantContent any
	}{
		{
			name:        "null JSON part fails normalization and becomes empty",
			content:     []any{nil},
			wantContent: "",
		},
		{
			name:        "unmarshalable dynamic part fails normalization and becomes empty",
			content:     []any{map[string]any{"type": "text", "text": func() {}}},
			wantContent: "",
		},
		{
			name:        "unknown dynamic part type fails normalization and becomes empty",
			content:     []any{map[string]any{"type": "thumbnail"}},
			wantContent: "",
		},
		{
			name:        "typed nil content in interface form becomes empty",
			content:     []core.ContentPart(nil),
			wantContent: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, _, err := ConvertMessagesToResponsesInput([]core.Message{
				{Role: "user", Content: tt.content},
			})
			if tt.wantContent == nil {
				require.Error(t, err)
				assert.Nil(t, input)
				return
			}
			require.NoError(t, err)
			items := responsesInputItems(t, input)
			require.Len(t, items, 1)
			assert.Equal(t, tt.wantContent, items[0].Content)
		})
	}
}

func TestConvertMessagesToResponsesInput_RefusalRebuildsAsContentPart(t *testing.T) {
	// An assistant chat message carrying a "refusal" extra is the
	// round-trip of a Responses refusal; the input converter must rebuild
	// it as a Responses content part of type "refusal" rather than strip
	// the member or pass it through as an unknown field.
	msg := core.Message{
		Role:    "assistant",
		Content: "",
		ExtraFields: chatViaResponsesExtras(map[string]string{
			"refusal": `"I cannot help with that."`,
		}),
	}
	input, _, err := ConvertMessagesToResponsesInput([]core.Message{msg})
	require.NoError(t, err)
	items := responsesInputItems(t, input)
	require.Len(t, items, 1)
	blocks, ok := items[0].Content.([]any)
	require.True(t, ok, "refusal-bearing message must emit a content-part array")
	require.Len(t, blocks, 1)
	block, ok := blocks[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "refusal", block["type"])
	assert.Equal(t, "I cannot help with that.", block["refusal"])
	assert.False(t, items[0].ExtraFields.HasAny("refusal"),
		"refusal must move out of extras into the content part")
}

func TestConvertMessagesToResponsesInput_RefusalBadJSONDropped(t *testing.T) {
	// A refusal extra that is not a JSON string is dropped rather than
	// turning the message into an invalid Responses item.
	msg := core.Message{
		Role: "assistant",
		ExtraFields: chatViaResponsesExtras(map[string]string{
			"refusal": `{"not":"a string"}`,
		}),
	}
	input, _, err := ConvertMessagesToResponsesInput([]core.Message{msg})
	require.NoError(t, err)
	items := responsesInputItems(t, input)
	require.Len(t, items, 1)
	assert.Equal(t, "", items[0].Content, "a malformed refusal must not produce a content part")
}

func TestConvertMessagesToResponsesInput_RebuildsEncryptedReasoning(t *testing.T) {
	// The output side relays reasoning.encrypted_content under the same
	// key; the input converter must rebuild the native Responses member
	// on the replay reasoning item and strip the chat-side wrapper.
	msg := core.Message{
		Role: "assistant",
		ExtraFields: chatViaResponsesExtras(map[string]string{
			core.ExtraContentField:        `{"o":"v"}`,
			"reasoning.encrypted_content": `"opaque-blob"`,
		}),
	}
	input, _, err := ConvertMessagesToResponsesInput([]core.Message{msg})
	require.NoError(t, err)
	items := responsesInputItems(t, input)
	require.NotEmpty(t, items)
	first := items[0]
	assert.Equal(t, "reasoning", first.Type)
	raw := first.ExtraFields.Lookup("encrypted_content")
	require.NotEmpty(t, raw, "encrypted_content must be rebuilt under the native Responses key")
	var blob string
	require.NoError(t, json.Unmarshal(raw, &blob))
	assert.Equal(t, "opaque-blob", blob)
	assert.False(t, first.ExtraFields.HasAny("reasoning.encrypted_content"),
		"chat-side wrapper must not travel onto the replay item")
}

func TestChatPartsToResponsesBlocks_DefaultFilenameForUnnamedFileData(t *testing.T) {
	// Inline file data without a filename would fail upstream; the
	// converter must fall back to the data-kind default, matching the
	// existing adapter.
	parts := []core.ContentPart{{
		Type: "file",
		File: &core.FileContent{FileData: "data:text/plain;base64,SGk="},
	}}
	input, _, err := ConvertMessagesToResponsesInput([]core.Message{{
		Role:    "user",
		Content: parts,
	}})
	require.NoError(t, err)
	items := responsesInputItems(t, input)
	require.Len(t, items, 1)
	blocks, ok := items[0].Content.([]any)
	require.True(t, ok)
	require.Len(t, blocks, 1)
	block, ok := blocks[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "input_file", block["type"])
	require.NotEmpty(t, block["filename"], "unnamed file data must inherit the default filename")
}

func TestConvertMessagesToResponsesInput_RejectsEmptyRole(t *testing.T) {
	// A chat message with an empty role is rejected before any
	// translation happens.
	_, _, err := ConvertMessagesToResponsesInput([]core.Message{
		{Role: "", Content: "hi"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "role is required")
}

func TestConvertMessagesToResponsesInput_RejectsMidConversationSystemUnknownPart(t *testing.T) {
	// A system/developer message after the first turn is no global
	// instruction; it travels as a Responses input item. When its
	// content part is of an unsupported type, the conversion error
	// surfaces here rather than being silently dropped.
	_, _, err := ConvertMessagesToResponsesInput([]core.Message{
		{Role: "user", Content: "hi"},
		{
			Role: "system",
			Content: []core.ContentPart{
				{Type: "image_url", ImageURL: &core.ImageURLContent{URL: "http://x/y.png"}},
				{Type: "totally_unknown_part"},
			},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "totally_unknown_part")
}
