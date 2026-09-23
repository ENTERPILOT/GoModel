package providers

import (
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestConvertResponsesResponseToChat_TextOnly(t *testing.T) {
	resp := &core.ResponsesResponse{
		ID:        "resp_upstream",
		Object:    "response",
		CreatedAt: 1730000000,
		Model:     "gpt-5",
		Status:    "completed",
		Output: []core.ResponsesOutputItem{
			{
				ID:     "msg_1",
				Type:   "message",
				Role:   "assistant",
				Status: "completed",
				Content: []core.ResponsesContentItem{
					{Type: "output_text", Text: "Hello, "},
					{Type: "output_text", Text: "world."},
				},
			},
		},
	}

	chat := ConvertResponsesResponseToChat(resp)
	require.NotNil(t, chat)
	require.Len(t, chat.Choices, 1)

	choice := chat.Choices[0]
	assert.Equal(t, 0, choice.Index)
	assert.Equal(t, "stop", choice.FinishReason)
	assert.Equal(t, "assistant", choice.Message.Role)
	assert.Equal(t, "Hello, world.", choice.Message.Content)
	assert.Empty(t, choice.Message.ToolCalls)
	assert.Equal(t, "gpt-5", chat.Model)
	assert.Equal(t, int64(1730000000), chat.Created)
	assert.Equal(t, "chat.completion", chat.Object)
}

func TestConvertResponsesResponseToChat_ToolCalls(t *testing.T) {
	tests := []struct {
		name  string
		items []core.ResponsesOutputItem
		want  []core.ToolCall
	}{
		{
			name: "single",
			items: []core.ResponsesOutputItem{
				{
					ID:        "fc_1",
					Type:      "function_call",
					Status:    "completed",
					CallID:    "call_abc",
					Name:      "lookup_weather",
					Arguments: `{"city":"Warsaw"}`,
				},
			},
			want: []core.ToolCall{{
				ID:       "call_abc",
				Type:     "function",
				Function: core.FunctionCall{Name: "lookup_weather", Arguments: `{"city":"Warsaw"}`},
			}},
		},
		{
			name: "parallel calls keep output order",
			items: []core.ResponsesOutputItem{
				{
					ID:        "fc_1",
					Type:      "function_call",
					Status:    "completed",
					CallID:    "call_1",
					Name:      "first",
					Arguments: `{}`,
				},
				{
					ID:        "fc_2",
					Type:      "function_call",
					Status:    "completed",
					CallID:    "call_2",
					Name:      "second",
					Arguments: `{"x":1}`,
				},
			},
			want: []core.ToolCall{
				{ID: "call_1", Type: "function", Function: core.FunctionCall{Name: "first", Arguments: `{}`}},
				{ID: "call_2", Type: "function", Function: core.FunctionCall{Name: "second", Arguments: `{"x":1}`}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &core.ResponsesResponse{
				ID:     "resp_upstream",
				Status: "completed",
				Output: tt.items,
			}

			chat := ConvertResponsesResponseToChat(resp)
			require.Len(t, chat.Choices, 1)

			choice := chat.Choices[0]
			assert.Equal(t, "tool_calls", choice.FinishReason)
			assert.Equal(t, tt.want, choice.Message.ToolCalls)
			assert.Equal(t, "", choice.Message.Content)
		})
	}
}

func TestConvertResponsesResponseToChat_ToolCallExtraContent(t *testing.T) {
	replay := json.RawMessage(`{"openai":{"item_reference":"fc_1"}}`)
	resp := &core.ResponsesResponse{
		ID:     "resp_upstream",
		Status: "completed",
		Output: []core.ResponsesOutputItem{
			{
				ID:        "fc_1",
				Type:      "function_call",
				Status:    "completed",
				CallID:    "call_abc",
				Name:      "lookup_weather",
				Arguments: `{"city":"Warsaw"}`,
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
					core.ExtraContentField: replay,
					"upstream_trace":       json.RawMessage(`"ignored"`),
				}),
			},
		},
	}

	chat := ConvertResponsesResponseToChat(resp)
	require.Len(t, chat.Choices, 1)
	require.Len(t, chat.Choices[0].Message.ToolCalls, 1)

	toolCall := chat.Choices[0].Message.ToolCalls[0]
	got := toolCall.ExtraFields.Lookup(core.ExtraContentField)
	require.NotEmpty(t, got, "extra_content replay state must land on the tool call")
	assert.JSONEq(t, string(replay), string(got))
	assert.Nil(t, toolCall.ExtraFields.Lookup("upstream_trace"), "only extra_content is replay state")
}

func TestConvertResponsesResponseToChat_Reasoning(t *testing.T) {
	replay := json.RawMessage(`{"anthropic":{"thinking_blocks":[{"type":"thinking","signature":"sig-1"}]}}`)
	resp := &core.ResponsesResponse{
		ID:     "resp_upstream",
		Status: "completed",
		Output: []core.ResponsesOutputItem{
			{
				ID:     "rs_1",
				Type:   "reasoning",
				Status: "completed",
				Content: []core.ResponsesContentItem{
					{Type: "reasoning_text", Text: "first thought"},
					{Type: "reasoning_text", Text: "second thought"},
				},
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
					core.ExtraContentField: replay,
					"summary":              json.RawMessage(`[]`),
				}),
			},
			{
				ID:      "msg_1",
				Type:    "message",
				Role:    "assistant",
				Status:  "completed",
				Content: []core.ResponsesContentItem{{Type: "output_text", Text: "answer"}},
			},
		},
	}

	chat := ConvertResponsesResponseToChat(resp)
	require.Len(t, chat.Choices, 1)
	message := chat.Choices[0].Message

	assert.Equal(t, "answer", message.Content)

	raw := message.ExtraFields.Lookup("reasoning_content")
	require.NotEmpty(t, raw, "reasoning item must surface as reasoning_content")
	var reasoning string
	require.NoError(t, json.Unmarshal(raw, &reasoning))
	assert.Equal(t, "first thought\n\nsecond thought", reasoning)

	gotReplay := message.ExtraFields.Lookup(core.ExtraContentField)
	assert.JSONEq(t, string(replay), string(gotReplay), "replay state must survive for the next translated request")
}

func TestConvertResponsesResponseToChat_ReasoningSummaryFallback(t *testing.T) {
	resp := &core.ResponsesResponse{
		ID:     "resp_upstream",
		Status: "completed",
		Output: []core.ResponsesOutputItem{
			{
				ID:   "rs_1",
				Type: "reasoning",
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
					"summary": json.RawMessage(`[{"type":"summary_text","text":"thought briefly"}]`),
				}),
			},
		},
	}

	chat := ConvertResponsesResponseToChat(resp)
	require.Len(t, chat.Choices, 1)

	raw := chat.Choices[0].Message.ExtraFields.Lookup("reasoning_content")
	require.NotEmpty(t, raw)
	var reasoning string
	require.NoError(t, json.Unmarshal(raw, &reasoning))
	assert.Equal(t, "thought briefly", reasoning)
}

func TestConvertResponsesResponseToChat_Refusal(t *testing.T) {
	resp := &core.ResponsesResponse{
		ID:     "resp_upstream",
		Status: "completed",
		Output: []core.ResponsesOutputItem{
			{
				ID:     "msg_1",
				Type:   "message",
				Role:   "assistant",
				Status: "completed",
				Content: []core.ResponsesContentItem{
					{Type: "refusal", Text: "I cannot help with that."},
				},
			},
		},
	}

	chat := ConvertResponsesResponseToChat(resp)
	require.Len(t, chat.Choices, 1)
	message := chat.Choices[0].Message

	assert.Equal(t, "", message.Content)
	raw := message.ExtraFields.Lookup("refusal")
	require.NotEmpty(t, raw, "refusal part must surface as the message refusal member")
	var refusal string
	require.NoError(t, json.Unmarshal(raw, &refusal))
	assert.Equal(t, "I cannot help with that.", refusal)
}

func TestConvertResponsesResponseToChat_FinishReason(t *testing.T) {
	functionCall := core.ResponsesOutputItem{
		ID:        "fc_1",
		Type:      "function_call",
		Status:    "completed",
		CallID:    "call_1",
		Name:      "f",
		Arguments: `{}`,
	}

	tests := []struct {
		name     string
		status   string
		reason   *core.ResponsesIncompleteDetails
		output   []core.ResponsesOutputItem
		expected string
	}{
		{name: "completed text", status: "completed", expected: "stop"},
		{name: "completed with function call", status: "completed", output: []core.ResponsesOutputItem{functionCall}, expected: "tool_calls"},
		{
			name:     "incomplete max_output_tokens",
			status:   "incomplete",
			reason:   &core.ResponsesIncompleteDetails{Reason: "max_output_tokens"},
			expected: "length",
		},
		{
			name:     "incomplete content_filter",
			status:   "incomplete",
			reason:   &core.ResponsesIncompleteDetails{Reason: "content_filter"},
			expected: "content_filter",
		},
		{
			name:     "incomplete unknown reason stays empty",
			status:   "incomplete",
			reason:   &core.ResponsesIncompleteDetails{Reason: "max_messages"},
			expected: "",
		},
		{name: "incomplete without details", status: "incomplete", expected: ""},
		{name: "failed stays empty for the caller", status: "failed", expected: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &core.ResponsesResponse{
				ID:                "resp_upstream",
				Status:            tt.status,
				IncompleteDetails: tt.reason,
				Output:            tt.output,
			}

			chat := ConvertResponsesResponseToChat(resp)
			require.Len(t, chat.Choices, 1)
			assert.Equal(t, tt.expected, chat.Choices[0].FinishReason)
		})
	}
}

func TestConvertResponsesResponseToChat_Usage(t *testing.T) {
	resp := &core.ResponsesResponse{
		ID:     "resp_upstream",
		Status: "completed",
		Usage: &core.ResponsesUsage{
			InputTokens:             12,
			OutputTokens:            7,
			TotalTokens:             19,
			PromptTokensDetails:     &core.PromptTokensDetails{CachedTokens: 5},
			CompletionTokensDetails: &core.CompletionTokensDetails{ReasoningTokens: 3},
		},
	}

	chat := ConvertResponsesResponseToChat(resp)
	require.NotNil(t, chat)

	assert.Equal(t, 12, chat.Usage.PromptTokens)
	assert.Equal(t, 7, chat.Usage.CompletionTokens)
	assert.Equal(t, 19, chat.Usage.TotalTokens)
	require.NotNil(t, chat.Usage.PromptTokensDetails)
	assert.Equal(t, 5, chat.Usage.PromptTokensDetails.CachedTokens)
	require.NotNil(t, chat.Usage.CompletionTokensDetails)
	assert.Equal(t, 3, chat.Usage.CompletionTokensDetails.ReasoningTokens)
}

func TestConvertResponsesResponseToChat_WithoutUsage(t *testing.T) {
	resp := &core.ResponsesResponse{ID: "resp_upstream", Status: "completed"}

	chat := ConvertResponsesResponseToChat(resp)
	require.NotNil(t, chat)
	assert.Equal(t, core.Usage{}, chat.Usage)
}

func TestConvertResponsesResponseToChat_MintsClientFacingID(t *testing.T) {
	resp := &core.ResponsesResponse{
		ID:     "resp_8f14e45fceea167a5a36dedd4bea2543",
		Status: "completed",
	}

	chat := ConvertResponsesResponseToChat(resp)
	require.NotNil(t, chat)

	assert.True(t, strings.HasPrefix(chat.ID, "chatcmpl-"), "client-facing ID must be chatcmpl- prefixed, got %q", chat.ID)
	assert.NotContains(t, chat.ID, resp.ID, "upstream resp_ ID must not leak onto the chat surface")
	assert.NotEqual(t, chat.ID, ConvertResponsesResponseToChat(resp).ID, "each conversion mints a fresh ID")
}
