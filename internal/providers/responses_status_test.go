package providers

import (
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestConvertChatResponseToResponses_StatusFromFinishReason(t *testing.T) {
	tests := []struct {
		name         string
		finishReason string
		wantStatus   string
		wantReason   string
	}{
		{name: "normal stop", finishReason: "stop", wantStatus: "completed"},
		{name: "tool calls", finishReason: "tool_calls", wantStatus: "completed"},
		{name: "truncated", finishReason: "length", wantStatus: "incomplete", wantReason: "max_output_tokens"},
		{name: "filtered", finishReason: "content_filter", wantStatus: "incomplete", wantReason: "content_filter"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := ConvertChatResponseToResponses(&core.ChatResponse{
				ID:    "chatcmpl-1",
				Model: "test-model",
				Choices: []core.Choice{{
					Message:      core.ResponseMessage{Role: "assistant", Content: "partial"},
					FinishReason: tt.finishReason,
				}},
			})
			require.Equal(t, tt.wantStatus, resp.Status)

			if tt.wantReason == "" {
				require.Nil(t, resp.IncompleteDetails)
				require.Equal(t, "completed", resp.Output[0].Status)

				return
			}
			require.NotNil(t, resp.IncompleteDetails)
			require.Equal(t, tt.wantReason, resp.IncompleteDetails.Reason)
			require.Equal(t, "incomplete", resp.Output[0].Status)
		})
	}
}

// A truncated response must serialize the OpenAI incomplete contract.
func TestConvertChatResponseToResponses_SerializesIncompleteDetails(t *testing.T) {
	resp := ConvertChatResponseToResponses(&core.ChatResponse{
		ID:      "chatcmpl-1",
		Model:   "test-model",
		Choices: []core.Choice{{Message: core.ResponseMessage{Role: "assistant", Content: "partial"}, FinishReason: "length"}},
	})
	encoded, err := json.Marshal(resp)
	require.NoError(t, err)

	var wire struct {
		Status            string `json:"status"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	}
	err = json.Unmarshal(encoded, &wire)
	require.NoError(t, err)
	require.Equal(t, "incomplete", wire.Status)
	require.NotNil(t, wire.IncompleteDetails)
	require.Equal(t, "max_output_tokens", wire.IncompleteDetails.Reason, "payload = %s, want incomplete with reason max_output_tokens", encoded)
}

// Provider usage extras must not reach the client on the Responses surface;
// reasoning tokens keep their OpenAI-shaped home.
func TestConvertChatResponseToResponses_NormalizesUsage(t *testing.T) {
	resp := ConvertChatResponseToResponses(&core.ChatResponse{
		ID:      "chatcmpl-1",
		Model:   "test-model",
		Choices: []core.Choice{{Message: core.ResponseMessage{Role: "assistant", Content: "hi"}, FinishReason: "stop"}},
		Usage: core.Usage{
			PromptTokens:            10,
			CompletionTokens:        7,
			TotalTokens:             17,
			CompletionTokensDetails: &core.CompletionTokensDetails{ReasoningTokens: 5},
			RawUsage: map[string]any{
				"thoughts_token_count":        5,
				"completion_reasoning_tokens": 5,
			},
		},
	})
	require.Equal(t, 5, resp.Usage.RawUsage["thoughts_token_count"], "RawUsage = %+v, want the provider extras kept for usage records", resp.Usage.RawUsage)

	encoded, err := json.Marshal(resp.Usage)
	require.NoError(t, err)

	var wire map[string]any
	err = json.Unmarshal(encoded, &wire)
	require.NoError(t, err)

	for _, key := range []string{"thoughts_token_count", "completion_reasoning_tokens", "raw_usage"} {
		_, exists := wire[key]
		require.False(t, exists, "usage payload %s carries %q, want the OpenAI Responses shape only", encoded, key)
	}
	details, ok := wire["output_tokens_details"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(5), details["reasoning_tokens"])
}

// An empty assistant answer must still serialize the required text member.
func TestBuildResponsesOutputItems_EmptyAnswerKeepsTextMember(t *testing.T) {
	items := BuildResponsesOutputItems(core.ResponseMessage{Role: "assistant", Content: ""})
	require.Len(t, items, 1)

	encoded, err := json.Marshal(items[0])
	require.NoError(t, err)

	var wire struct {
		Content []map[string]json.RawMessage `json:"content"`
	}
	err = json.Unmarshal(encoded, &wire)
	require.NoError(t, err)
	require.Len(t, wire.Content, 1, "content = %s, want one part", encoded)

	text, ok := wire.Content[0]["text"]
	require.True(t, ok)
	require.Equal(t, `""`, string(text), "output_text part = %s, want text \"\"", encoded)
}
