package usage

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEntryReasoningTokens(t *testing.T) {
	tests := []struct {
		name         string
		provider     string
		rawData      map[string]any
		wantTokens   int64
		wantIncluded bool
	}{
		{name: "none reported", provider: "openai", rawData: map[string]any{"prompt_cached_tokens": 10}},
		{name: "openai counts reasoning inside output", provider: "openai", rawData: map[string]any{"completion_reasoning_tokens": 40}, wantTokens: 40, wantIncluded: true},
		{name: "unlisted provider follows the openai schema", provider: "deepseek", rawData: map[string]any{"reasoning_tokens": 7}, wantTokens: 7, wantIncluded: true},
		{name: "anthropic thinking tokens", provider: "anthropic", rawData: map[string]any{"completion_thinking_tokens": 12}, wantTokens: 12, wantIncluded: true},
		{name: "gemini thought tokens", provider: "gemini", rawData: map[string]any{"thought_tokens": 9.0}, wantTokens: 9, wantIncluded: true},
		{name: "xai reports reasoning on top of output", provider: "xai", rawData: map[string]any{"reasoning_tokens": 30}, wantTokens: 30},
		{name: "aliases of one count are not added up", provider: "openai", rawData: map[string]any{"reasoning_tokens": 5, "completion_reasoning_tokens": 5}, wantTokens: 5, wantIncluded: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokens, included := EntryReasoningTokens(tt.provider, tt.rawData)
			assert.Equal(t, tt.wantTokens, tokens)
			assert.Equal(t, tt.wantIncluded, included)
		})
	}
}
