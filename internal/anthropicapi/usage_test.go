package anthropicapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// The Anthropic input_tokens excludes cached tokens, while OpenAI's
// prompt_tokens includes them. Each upstream usage object must render the same
// Anthropic usage through the response and the stream converter.
func TestAnthropicUsageMatchesAcrossResponseAndStream(t *testing.T) {
	tests := []struct {
		name  string
		usage string
		want  Usage
	}{
		{
			name:  "openai cache read and write",
			usage: `{"prompt_tokens":1000,"completion_tokens":50,"prompt_tokens_details":{"cached_tokens":800,"cache_write_tokens":150}}`,
			want:  Usage{InputTokens: 50, OutputTokens: 50, CacheReadInputTokens: 800, CacheCreationInputTokens: 150},
		},
		{
			name:  "openai without cache",
			usage: `{"prompt_tokens":12,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":0}}`,
			want:  Usage{InputTokens: 12, OutputTokens: 3},
		},
		{
			name:  "openai cache count above prompt clamps at zero",
			usage: `{"prompt_tokens":10,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":12}}`,
			want:  Usage{InputTokens: 0, OutputTokens: 1, CacheReadInputTokens: 12},
		},
		{
			name:  "anthropic-shaped counts pass through",
			usage: `{"prompt_tokens":100,"completion_tokens":20,"cache_read_input_tokens":40,"cache_creation_input_tokens":30}`,
			want:  Usage{InputTokens: 100, OutputTokens: 20, CacheReadInputTokens: 40, CacheCreationInputTokens: 30},
		},
		{
			name:  "no details",
			usage: `{"prompt_tokens":7,"completion_tokens":2}`,
			want:  Usage{InputTokens: 7, OutputTokens: 2},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var usage core.Usage
			require.NoError(t, json.Unmarshal([]byte(tt.usage), &usage))
			resp := FromChatResponse(&core.ChatResponse{
				Usage:   usage,
				Choices: []core.Choice{{Message: core.ResponseMessage{Role: "assistant", Content: "hi"}, FinishReason: "stop"}},
			})
			assert.Equal(t, tt.want, resp.Usage, "response usage")

			events := drainConverter(t, strings.Join([]string{
				`data: {"id":"chatcmpl-1","model":"gpt","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`,
				`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
				`data: {"choices":[],"usage":` + tt.usage + `}`,
				`data: [DONE]`,
				"",
			}, "\n\n"))
			var streamed Usage
			for _, event := range events {
				if event["type"] != "message_delta" {
					continue
				}
				raw, err := json.Marshal(event["usage"])
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(raw, &streamed))
			}
			assert.Equal(t, tt.want, streamed, "stream usage")
		})
	}
}
