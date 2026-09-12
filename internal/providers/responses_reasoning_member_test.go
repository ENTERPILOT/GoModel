package providers

import (
	"io"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// Providers that name the member "reasoning" instead of "reasoning_content"
// (Groq's parsed reasoning_format, OpenRouter) must still produce a reasoning
// item on the Responses API, with reasoning_content winning when both arrive.
func TestResponsesReasoningFromVendorReasoningMember(t *testing.T) {
	tests := []struct {
		name  string
		delta string
		want  string
	}{
		{name: "reasoning alone", delta: `{"reasoning":"Think."}`, want: "Think."},
		{name: "reasoning_content wins", delta: `{"reasoning_content":"Canonical.","reasoning":"Vendor."}`, want: "Canonical."},
		// A provider that sends a non-string reasoning_content (an effort echo,
		// say) must not suppress the vendor member that carries the text.
		{name: "a non-string reasoning_content falls back", delta: `{"reasoning_content":{"effort":"high"},"reasoning":"Vendor."}`, want: "Vendor."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStream := `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"qwen/qwen3.6-27b","choices":[{"index":0,"delta":` + tt.delta + `,"finish_reason":null}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"qwen/qwen3.6-27b","choices":[{"index":0,"delta":{"content":"391"},"finish_reason":"stop"}]}

data: [DONE]
`
			converter := NewOpenAIResponsesStreamConverter(io.NopCloser(strings.NewReader(mockStream)), "qwen/qwen3.6-27b", "groq")
			raw, err := io.ReadAll(converter)
			require.NoError(t, err)

			var got strings.Builder
			for _, event := range parseTestSSEEvents(t, string(raw)) {
				if event.Done || !strings.HasSuffix(event.Name, "reasoning_text.delta") {
					continue
				}
				delta, _ := event.Payload["delta"].(string)
				got.WriteString(delta)
			}
			assert.Equal(t, tt.want, got.String())
		})
	}
}

func TestConvertChatResponseToResponsesReadsVendorReasoningMember(t *testing.T) {
	tests := []struct {
		name   string
		fields map[string]json.RawMessage
		want   string
	}{
		{name: "reasoning alone", fields: map[string]json.RawMessage{"reasoning": json.RawMessage(`"Think."`)}, want: "Think."},
		{
			name: "reasoning_content wins",
			fields: map[string]json.RawMessage{
				"reasoning_content": json.RawMessage(`"Canonical."`),
				"reasoning":         json.RawMessage(`"Vendor."`),
			},
			want: "Canonical.",
		},
		{name: "a non-string member is ignored", fields: map[string]json.RawMessage{"reasoning": json.RawMessage(`{"effort":"high"}`)}},
		{
			name: "a non-string reasoning_content falls back",
			fields: map[string]json.RawMessage{
				"reasoning_content": json.RawMessage(`{"effort":"high"}`),
				"reasoning":         json.RawMessage(`"Vendor."`),
			},
			want: "Vendor.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &core.ChatResponse{Choices: []core.Choice{{
				Message: core.ResponseMessage{
					Role:        "assistant",
					Content:     "391",
					ExtraFields: core.UnknownJSONFieldsFromMap(tt.fields),
				},
				FinishReason: "stop",
			}}}
			var got string
			for _, item := range ConvertChatResponseToResponses(resp).Output {
				if item.Type != "reasoning" {
					continue
				}
				for _, part := range item.Content {
					got += part.Text
				}
			}
			assert.Equal(t, tt.want, got)
		})
	}
}
