package usage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestEntryReasoningTokens(t *testing.T) {
	tests := []struct {
		name         string
		entry        UsageLogEntry
		wantTokens   int64
		wantIncluded bool
	}{
		{name: "none reported", entry: UsageLogEntry{Provider: "openai", RawData: map[string]any{"prompt_cached_tokens": 10}}, wantIncluded: true},
		{name: "openai counts reasoning inside output", entry: UsageLogEntry{Provider: "openai", RawData: map[string]any{"completion_reasoning_tokens": 40}}, wantTokens: 40, wantIncluded: true},
		{name: "unlisted provider follows the openai schema", entry: UsageLogEntry{Provider: "deepseek", RawData: map[string]any{"reasoning_tokens": 7}}, wantTokens: 7, wantIncluded: true},
		{name: "anthropic thinking tokens", entry: UsageLogEntry{Provider: "anthropic", RawData: map[string]any{"completion_thinking_tokens": 12}}, wantTokens: 12, wantIncluded: true},
		{name: "gemini thought tokens", entry: UsageLogEntry{Provider: "gemini", RawData: map[string]any{"thought_tokens": 9.0}}, wantTokens: 9, wantIncluded: true},
		{name: "aliases of one count are not added up", entry: UsageLogEntry{Provider: "openai", RawData: map[string]any{"reasoning_tokens": 5, "completion_reasoning_tokens": 5}}, wantTokens: 5, wantIncluded: true},
		{
			name:       "xai usage without a total is not counted twice",
			entry:      UsageLogEntry{Provider: "xai", InputTokens: 10, OutputTokens: 35, RawData: map[string]any{"reasoning_tokens": 30}},
			wantTokens: 30, wantIncluded: true,
		},
		{
			name:       "a total with room for reasoning means nothing for other providers",
			entry:      UsageLogEntry{Provider: "openai", InputTokens: 10, OutputTokens: 35, TotalTokens: 420, RawData: map[string]any{"completion_reasoning_tokens": 375}},
			wantTokens: 375, wantIncluded: true,
		},
		{
			// The native adapter has already added thoughts to the output, so a
			// total that counts anything else must not add them again.
			name:       "a gemini total with room for reasoning does not add it again",
			entry:      UsageLogEntry{Provider: "gemini", InputTokens: 42, OutputTokens: 29, TotalTokens: 101, RawData: map[string]any{"thoughts_token_count": 17, "completion_reasoning_tokens": 17, "tool_use_prompt_token_count": 30}},
			wantTokens: 17, wantIncluded: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokens, included := EntryReasoningTokens(tt.entry)
			assert.Equal(t, tt.wantTokens, tokens)
			assert.Equal(t, tt.wantIncluded, included)
		})
	}
}

// Every recorded usage object that carries reasoning tokens, as it reaches a
// usage entry. The output count a request is charged for, when its row is
// written and when it is repriced, is the one the metric exports.
func TestRecordedReasoningUsage(t *testing.T) {
	tests := []struct {
		name                 string
		provider, endpoint   string
		input, output, total int
		rawData              map[string]any
		wantReasoning        int64
		// wantOutput is the output with reasoning counted in.
		wantOutput int
	}{
		{
			// tests/contract/testdata/openai/chat_completion_reasoning.json: 43 + 148 = 191.
			name: "openai chat completions", provider: "openai", endpoint: "/v1/chat/completions",
			input: 43, output: 148, total: 191, rawData: map[string]any{"completion_reasoning_tokens": 128},
			wantReasoning: 128, wantOutput: 148,
		},
		{
			// tests/contract/testdata/xai/chat_completion.json: 10 + 35 + 375 = 420.
			name: "xai chat completions", provider: "xai", endpoint: "/v1/chat/completions",
			input: 10, output: 35, total: 420, rawData: map[string]any{"completion_reasoning_tokens": 375},
			wantReasoning: 375, wantOutput: 410,
		},
		{
			// tests/contract/testdata/xai/chat_with_params.json: 19 + 6 + 173 = 198.
			name: "xai chat completions with parameters", provider: "xai", endpoint: "/v1/chat/completions",
			input: 19, output: 6, total: 198, rawData: map[string]any{"completion_reasoning_tokens": 173},
			wantReasoning: 173, wantOutput: 179,
		},
		{
			// tests/contract/testdata/xai/responses.json: 17 + 276 = 293.
			name: "xai responses", provider: "xai", endpoint: "/v1/responses",
			input: 17, output: 276, total: 293, rawData: map[string]any{"completion_reasoning_tokens": 272},
			wantReasoning: 272, wantOutput: 276,
		},
		{
			// tests/contract/testdata/xai/responses_stream.txt: 17 + 185 = 202.
			name: "xai streamed responses", provider: "xai", endpoint: "/v1/responses",
			input: 17, output: 185, total: 202, rawData: map[string]any{"completion_reasoning_tokens": 181},
			wantReasoning: 181, wantOutput: 185,
		},
		{
			// tests/contract/testdata/gemini/native_chat_completion.json reports
			// 11 + 4 + 21 = 36; the adapter adds the thoughts to the completion
			// tokens (golden/gemini/native_chat_completion.golden.json: 11 + 25 = 36).
			name: "gemini native api", provider: "gemini", endpoint: "/v1/chat/completions",
			input: 11, output: 25, total: 36, rawData: map[string]any{"thoughts_token_count": 21, "completion_reasoning_tokens": 21},
			wantReasoning: 21, wantOutput: 25,
		},
		{
			// tests/contract/testdata/gemini/native_tool_call_signed.json reports
			// 42 + 12 + 17 = 71, which the adapter turns into 42 + 29 = 71.
			name: "gemini native api with a tool call", provider: "gemini", endpoint: "/v1/chat/completions",
			input: 42, output: 29, total: 71, rawData: map[string]any{"thoughts_token_count": 17, "completion_reasoning_tokens": 17},
			wantReasoning: 17, wantOutput: 29,
		},
		{
			// tests/contract/testdata/anthropic/messages_thinking_tool_use.json:
			// output_tokens 88 holds 42 thinking tokens; the adapter adds the total.
			name: "anthropic messages", provider: "anthropic", endpoint: "/v1/chat/completions",
			input: 61, output: 88, total: 149, rawData: map[string]any{"completion_reasoning_tokens": 42},
			wantReasoning: 42, wantOutput: 88,
		},
		{
			// Live capture in internal/providers/minimax/reasoning_stream_test.go: 769 + 116 = 885.
			name: "minimax chat completions", provider: "minimax", endpoint: "/v1/chat/completions",
			input: 769, output: 116, total: 885, rawData: map[string]any{"prompt_cached_tokens": 443, "completion_reasoning_tokens": 37},
			wantReasoning: 37, wantOutput: 116,
		},
	}
	// One USD per output token, so the output cost is the count charged for.
	pricing := &core.ModelPricing{OutputPerMtok: new(1_000_000.0)}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reasoning, included := EntryReasoningTokens(UsageLogEntry{
				Provider: tt.provider, Endpoint: tt.endpoint,
				InputTokens: tt.input, OutputTokens: tt.output, TotalTokens: tt.total, RawData: tt.rawData,
			})
			assert.Equal(t, tt.wantReasoning, reasoning)
			assert.Equal(t, tt.wantOutput == tt.output, included)

			entry := ExtractFromSSEUsage("id", tt.input, tt.output, tt.total, tt.rawData, "req", "model", tt.provider, tt.endpoint, pricing)
			require.NotNil(t, entry)
			assert.Equal(t, tt.output, entry.OutputTokens, "the usage log keeps the count the provider reported")
			assertCostNear(t, "OutputCost", entry.OutputCost, float64(tt.wantOutput))

			repriced := recalculateEntryCosts(recalculationEntry{
				Model: "model", Provider: tt.provider, Endpoint: tt.endpoint,
				InputTokens: tt.input, OutputTokens: tt.output, TotalTokens: tt.total, RawData: tt.rawData,
			}, &recordingPricingResolver{pricing: pricing})
			assertCostNear(t, "repriced OutputCost", repriced.OutputCost, float64(tt.wantOutput))
		})
	}
}
