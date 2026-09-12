package providers

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
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
			require.NoError(t, err)

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

			require.NotNil(t, response, "expected %s terminal event, got %s", tt.wantEvent, raw)

			if tt.wantReason == "" {
				require.Equal(t, "completed", response["status"])
				_, exists := response["incomplete_details"]
				require.False(t, exists, "incomplete_details = %#v, want none", response["incomplete_details"])
				require.Equal(t, "completed", itemStatus)

				return
			}
			require.Equal(t, "incomplete", response["status"])

			details, _ := response["incomplete_details"].(map[string]any)
			require.Equal(t, tt.wantReason, details["reason"], "incomplete_details = %#v, want reason %q", response["incomplete_details"], tt.wantReason)
			require.Equal(t, "incomplete", itemStatus)
		})
	}
}
