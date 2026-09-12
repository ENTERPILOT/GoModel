package anthropic

import (
	"io"
	"strings"
	"testing"

	"github.com/goccy/go-json"
)

// A turn Anthropic cut at max_tokens is an incomplete response, not a
// completed one.
func TestConvertAnthropicResponseToResponses_MaxTokensIsIncomplete(t *testing.T) {
	resp := convertAnthropicResponseToResponses(&anthropicResponse{
		ID:         "msg_1",
		StopReason: "max_tokens",
		Content:    []anthropicContent{{Type: "text", Text: "partial"}},
	}, "claude-haiku-4-5")

	if resp.Status != "incomplete" {
		t.Fatalf("status = %q, want incomplete", resp.Status)
	}
	if resp.IncompleteDetails == nil || resp.IncompleteDetails.Reason != "max_output_tokens" {
		t.Fatalf("incomplete_details = %+v, want reason max_output_tokens", resp.IncompleteDetails)
	}
	if resp.Output[0].Status != "incomplete" {
		t.Fatalf("message item status = %q, want incomplete", resp.Output[0].Status)
	}
}

func TestConvertAnthropicResponseToResponses_EndTurnCompletes(t *testing.T) {
	resp := convertAnthropicResponseToResponses(&anthropicResponse{
		ID:         "msg_1",
		StopReason: "end_turn",
		Content:    []anthropicContent{{Type: "text", Text: "done"}},
	}, "claude-haiku-4-5")

	if resp.Status != "completed" || resp.IncompleteDetails != nil {
		t.Fatalf("status = %q, incomplete_details = %+v, want a completed response", resp.Status, resp.IncompleteDetails)
	}
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
	if usage.PromptTokensDetails == nil || usage.PromptTokensDetails.CachedTokens != 40 {
		t.Fatalf("input token details = %+v, want cached_tokens 40", usage.PromptTokensDetails)
	}
	if usage.CompletionTokensDetails == nil || usage.CompletionTokensDetails.ReasoningTokens != 12 {
		t.Fatalf("output token details = %+v, want reasoning_tokens 12", usage.CompletionTokensDetails)
	}
	if usage.RawUsage["cache_creation_input_tokens"] != 10 {
		t.Fatalf("RawUsage = %+v, want the Anthropic counts kept for usage records", usage.RawUsage)
	}

	encoded, err := json.Marshal(usage)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"cache_read_input_tokens", "cache_creation_input_tokens", "completion_reasoning_tokens"} {
		if _, exists := wire[key]; exists {
			t.Fatalf("usage payload %s carries %q, want the OpenAI Responses shape only", encoded, key)
		}
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
	if err != nil {
		t.Fatalf("failed to read from converter: %v", err)
	}

	var response map[string]any
	itemStatus := ""
	for _, event := range parseTestSSEEvents(t, string(raw)) {
		switch event.Name {
		case "response.completed":
			t.Fatalf("truncated stream ended with response.completed: %s", raw)
		case "response.incomplete":
			response, _ = event.Payload["response"].(map[string]any)
		case "response.output_item.done":
			if item, _ := event.Payload["item"].(map[string]any); item["type"] == "message" {
				itemStatus, _ = item["status"].(string)
			}
		}
	}
	if response == nil {
		t.Fatalf("expected response.incomplete terminal event: %s", raw)
	}
	details, _ := response["incomplete_details"].(map[string]any)
	if details["reason"] != "max_output_tokens" {
		t.Fatalf("incomplete_details = %#v, want reason max_output_tokens", response["incomplete_details"])
	}
	if itemStatus != "incomplete" {
		t.Fatalf("message item status = %q, want incomplete", itemStatus)
	}
}
