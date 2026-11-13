package anthropic

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// The translation's own additions (the default limit, the room above a
// thinking budget) stay within the caller's output token ceiling.
func TestWithinRequestCeilings(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		maxTokens  *int
		effort     string
		ceiling    int
		envDefault string
		wantTokens int
		// wantThinking is the thinking block sent: nil for none.
		wantThinking *anthropicThinking
	}{
		{name: "the default limit is clamped to the ceiling", model: "claude-sonnet-4-5", ceiling: 1000, wantTokens: 1000},
		{name: "a configured default is clamped too", model: "claude-sonnet-4-5", ceiling: 1000, envDefault: "64000", wantTokens: 1000},
		{name: "a limit within the ceiling is kept", model: "claude-sonnet-4-5", maxTokens: new(500), ceiling: 1000, wantTokens: 500},
		{
			name: "no budget fits: thinking is off", model: "claude-sonnet-4-5", maxTokens: new(1000), effort: "high", ceiling: 1000,
			wantTokens: 1000,
		},
		{
			name: "no budget fits on a model that thinks by default: thinking is disabled", model: "claude-haiku-5-5", maxTokens: new(1000), effort: "low", ceiling: 1000,
			wantTokens: 1000, wantThinking: &anthropicThinking{Type: "disabled"},
		},
		{
			name: "the budget shrinks to leave room for the answer", model: "claude-sonnet-4-5", maxTokens: new(8000), effort: "high", ceiling: 8000,
			wantTokens: 8000, wantThinking: &anthropicThinking{Type: "enabled", BudgetTokens: 8000 - thinkingAnswerRoom},
		},
		{
			name: "a tight limit still fits the minimum budget", model: "claude-sonnet-4-5", maxTokens: new(1500), effort: "low", ceiling: 1500,
			wantTokens: 1500, wantThinking: &anthropicThinking{Type: "enabled", BudgetTokens: minThinkingBudget},
		},
		{
			name: "a budget that fits is kept", model: "claude-sonnet-4-5", maxTokens: new(30000), effort: "high", ceiling: 30000,
			wantTokens: 30000, wantThinking: &anthropicThinking{Type: "enabled", BudgetTokens: 20000},
		},
		{
			name: "without a ceiling max_tokens is raised as before", model: "claude-sonnet-4-5", maxTokens: new(1000), effort: "high",
			wantTokens: 21024, wantThinking: &anthropicThinking{Type: "enabled", BudgetTokens: 20000},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(defaultMaxTokensEnvVar, tt.envDefault)
			req := &core.ChatRequest{Model: tt.model, MaxTokens: tt.maxTokens, Messages: []core.Message{{Role: "user", Content: "hi"}}}
			if tt.effort != "" {
				req.Reasoning = &core.Reasoning{Effort: tt.effort}
			}
			translated, err := convertToAnthropicRequest(req)
			require.NoError(t, err)

			ctx := core.WithRequestCeilings(context.Background(), core.RequestCeilings{OutputTokens: tt.ceiling})
			withinRequestCeilings(ctx, translated)

			assert.Equal(t, tt.wantTokens, translated.MaxTokens)
			assert.Equal(t, tt.wantThinking, translated.Thinking)
		})
	}
}
