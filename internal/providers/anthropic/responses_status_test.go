package anthropic

import (
	"io"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A turn Anthropic cut at max_tokens is an incomplete response, not a
// completed one.
func TestConvertAnthropicResponseToResponses_MaxTokensIsIncomplete(t *testing.T) {
	resp := convertAnthropicResponseToResponses(&anthropicResponse{
		ID:         "msg_1",
		StopReason: "max_tokens",
		Content:    []anthropicContent{{Type: "text", Text: "partial"}},
	}, "claude-haiku-4-5")

	assert.Equal(t, "incomplete", resp.Status)
	require.NotNil(t, resp.IncompleteDetails)
	assert.Equal(t, "max_output_tokens", resp.IncompleteDetails.Reason)
	assert.Equal(t, "incomplete", resp.Output[0].Status)
}

func TestConvertAnthropicResponseToResponses_EndTurnCompletes(t *testing.T) {
	resp := convertAnthropicResponseToResponses(&anthropicResponse{
		ID:         "msg_1",
		StopReason: "end_turn",
		Content:    []anthropicContent{{Type: "text", Text: "done"}},
	}, "claude-haiku-4-5")

	assert.Equal(t, "completed", resp.Status)
	assert.Nil(t, resp.IncompleteDetails)
}

// Cache reads and thinking tokens keep their OpenAI-shaped home; the
// Anthropic-named counts stay out of the client-visible usage object.
func TestBuildAnthropicResponsesUsage_NormalizesDetails(t *testing.T) {
	usage := buildAnthropicResponsesUsage(anthropicUsage{
		InputTokens:              100,
		OutputTokens:             20,
		CacheReadInputTokens:     40,
		CacheCreationInputTokens: 10,
		OutputTokensDetails:      anthropicOutputTokensDetails{ThinkingTokens: 12},
	})
	require.NotNil(t, usage.PromptTokensDetails)
	assert.Equal(t, 40, usage.PromptTokensDetails.CachedTokens)
	require.NotNil(t, usage.CompletionTokensDetails)
	assert.Equal(t, 12, usage.CompletionTokensDetails.ReasoningTokens)
	assert.Equal(t, 10, usage.RawUsage["cache_creation_input_tokens"], "Anthropic counts must stay in RawUsage for usage records")

	encoded, err := json.Marshal(usage)
	require.NoError(t, err)

	var wire map[string]any
	err = json.Unmarshal(encoded, &wire)
	require.NoError(t, err)

	for _, key := range []string{"cache_read_input_tokens", "cache_creation_input_tokens", "completion_reasoning_tokens"} {
		assert.NotContains(t, wire, key, "usage payload must keep the OpenAI Responses shape only")
	}
}

// A streamed turn stopped at max_tokens ends with response.incomplete.
func TestResponsesStreamConverter_MaxTokensEndsIncomplete(t *testing.T) {
	stream := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":5}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":4}}

event: message_stop
data: {"type":"message_stop"}

`
	converter := newResponsesStreamConverter(io.NopCloser(strings.NewReader(stream)), "claude-haiku-4-5")
	raw, err := io.ReadAll(converter)
	require.NoError(t, err)

	var response map[string]any
	itemStatus := ""
	for _, event := range parseTestSSEEvents(t, string(raw)) {
		switch event.Name {
		case "response.completed":
			require.Fail(t, "truncated stream ended with response.completed", "%s", raw)
		case "response.incomplete":
			response, _ = event.Payload["response"].(map[string]any)
		case "response.output_item.done":
			if item, _ := event.Payload["item"].(map[string]any); item["type"] == "message" {
				itemStatus, _ = item["status"].(string)
			}
		}
	}
	require.NotNil(t, response)

	details, _ := response["incomplete_details"].(map[string]any)
	assert.Equal(t, "max_output_tokens", details["reason"])
	assert.Equal(t, "incomplete", itemStatus)
}
