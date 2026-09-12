package providers

import (
	"io"
	"strings"
	"testing"
)

// A translated stream that hits the token limit must end with
// response.incomplete and the OpenAI reason, not fabricate completion.
func TestOpenAIResponsesStreamConverter_FinishReasonEndsIncomplete(t *testing.T) {
	tests := []struct {
		name         string
		finishReason string
		wantEvent    string
		wantReason   string
	}{
		{name: "stop", finishReason: "stop", wantEvent: "response.completed"},
		{name: "length", finishReason: "length", wantEvent: "response.incomplete", wantReason: "max_output_tokens"},
		{name: "content filter", finishReason: "content_filter", wantEvent: "response.incomplete", wantReason: "content_filter"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStream := `data: {"choices":[{"delta":{"content":"Hel"},"finish_reason":null}]}

data: {"choices":[{"delta":{"content":"lo"},"finish_reason":"` + tt.finishReason + `"}]}

data: [DONE]
`
			converter := NewOpenAIResponsesStreamConverter(io.NopCloser(strings.NewReader(mockStream)), "test-model", "mock")
			raw, err := io.ReadAll(converter)
			if err != nil {
				t.Fatalf("ReadAll() error = %v", err)
			}

			itemStatus := ""
			var response map[string]any
			for _, event := range parseTestSSEEvents(t, string(raw)) {
				switch event.Name {
				case "response.output_item.done":
					if item, _ := event.Payload["item"].(map[string]any); item["type"] == "message" {
						itemStatus, _ = item["status"].(string)
					}
				case tt.wantEvent:
					response, _ = event.Payload["response"].(map[string]any)
				}
			}

			if response == nil {
				t.Fatalf("expected %s terminal event, got %s", tt.wantEvent, raw)
			}
			if tt.wantReason == "" {
				if response["status"] != "completed" {
					t.Fatalf("response.status = %v, want completed", response["status"])
				}
				if _, exists := response["incomplete_details"]; exists {
					t.Fatalf("incomplete_details = %#v, want none", response["incomplete_details"])
				}
				if itemStatus != "completed" {
					t.Fatalf("message item status = %q, want completed", itemStatus)
				}
				return
			}
			if response["status"] != "incomplete" {
				t.Fatalf("response.status = %v, want incomplete", response["status"])
			}
			details, _ := response["incomplete_details"].(map[string]any)
			if details["reason"] != tt.wantReason {
				t.Fatalf("incomplete_details = %#v, want reason %q", response["incomplete_details"], tt.wantReason)
			}
			if itemStatus != "incomplete" {
				t.Fatalf("message item status = %q, want incomplete", itemStatus)
			}
		})
	}
}
