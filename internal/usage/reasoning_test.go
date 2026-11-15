package usage

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
			// tests/contract/testdata/xai/chat_completion.json: 10 + 35 + 375 = 420.
			name:       "xai chat completions report reasoning on top of output",
			entry:      UsageLogEntry{Provider: "xai", Endpoint: "/v1/chat/completions", InputTokens: 10, OutputTokens: 35, TotalTokens: 420, RawData: map[string]any{"completion_reasoning_tokens": 375}},
			wantTokens: 375,
		},
		{
			// tests/contract/testdata/xai/responses.json: 17 + 276 = 293.
			name:       "xai responses count reasoning inside output",
			entry:      UsageLogEntry{Provider: "xai", Endpoint: "/v1/responses", InputTokens: 17, OutputTokens: 276, TotalTokens: 293, RawData: map[string]any{"completion_reasoning_tokens": 272}},
			wantTokens: 272, wantIncluded: true,
		},
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokens, included := EntryReasoningTokens(tt.entry)
			assert.Equal(t, tt.wantTokens, tokens)
			assert.Equal(t, tt.wantIncluded, included)
		})
	}
}
