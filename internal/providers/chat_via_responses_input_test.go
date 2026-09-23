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
			name:             "empty message list",
			messages:         nil,
			wantInstructions: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, instructions, err := ConvertMessagesToResponsesInput(tt.messages)
			require.NoError(t, err)
			assert.Equal(t, tt.wantInstructions, instructions)
			if len(tt.wantItems) == 0 {
				assert.Nil(t, input)
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
	assert.Equal(t, "", instructions)

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
