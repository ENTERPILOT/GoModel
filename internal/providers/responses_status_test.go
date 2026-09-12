package providers

import (
	"testing"

	"github.com/goccy/go-json"

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
			if resp.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", resp.Status, tt.wantStatus)
			}
			if tt.wantReason == "" {
				if resp.IncompleteDetails != nil {
					t.Fatalf("incomplete_details = %+v, want none", resp.IncompleteDetails)
				}
				if resp.Output[0].Status != "completed" {
					t.Fatalf("message item status = %q, want completed", resp.Output[0].Status)
				}
				return
			}
			if resp.IncompleteDetails == nil || resp.IncompleteDetails.Reason != tt.wantReason {
				t.Fatalf("incomplete_details = %+v, want reason %q", resp.IncompleteDetails, tt.wantReason)
			}
			if resp.Output[0].Status != "incomplete" {
				t.Fatalf("message item status = %q, want incomplete", resp.Output[0].Status)
			}
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
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire struct {
		Status            string `json:"status"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if wire.Status != "incomplete" || wire.IncompleteDetails == nil || wire.IncompleteDetails.Reason != "max_output_tokens" {
		t.Fatalf("payload = %s, want incomplete with reason max_output_tokens", encoded)
	}
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
	if resp.Usage.RawUsage["thoughts_token_count"] != 5 {
		t.Fatalf("RawUsage = %+v, want the provider extras kept for usage records", resp.Usage.RawUsage)
	}
	encoded, err := json.Marshal(resp.Usage)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"thoughts_token_count", "completion_reasoning_tokens", "raw_usage"} {
		if _, exists := wire[key]; exists {
			t.Fatalf("usage payload %s carries %q, want the OpenAI Responses shape only", encoded, key)
		}
	}
	details, ok := wire["output_tokens_details"].(map[string]any)
	if !ok || details["reasoning_tokens"] != float64(5) {
		t.Fatalf("output_tokens_details = %#v, want reasoning_tokens 5", wire["output_tokens_details"])
	}
}

// An empty assistant answer must still serialize the required text member.
func TestBuildResponsesOutputItems_EmptyAnswerKeepsTextMember(t *testing.T) {
	items := BuildResponsesOutputItems(core.ResponseMessage{Role: "assistant", Content: ""})
	if len(items) != 1 {
		t.Fatalf("items = %+v, want one message item", items)
	}
	encoded, err := json.Marshal(items[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire struct {
		Content []map[string]json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(wire.Content) != 1 {
		t.Fatalf("content = %s, want one part", encoded)
	}
	text, ok := wire.Content[0]["text"]
	if !ok || string(text) != `""` {
		t.Fatalf("output_text part = %s, want text \"\"", encoded)
	}
}
