package anthropic

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/anthropicapi"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/requestparams"
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
		// wantEffort is the output_config.effort sent: "" for none.
		wantEffort string
	}{
		{name: "the default limit is clamped to the ceiling", model: "claude-sonnet-4-5", ceiling: 1000, wantTokens: 1000},
		{name: "a configured default is clamped too", model: "claude-sonnet-4-5", ceiling: 1000, envDefault: "64000", wantTokens: 1000},
		{name: "a limit within the ceiling is kept", model: "claude-sonnet-4-5", maxTokens: new(500), ceiling: 1000, wantTokens: 500},
		{
			name: "no budget fits: thinking is off", model: "claude-sonnet-4-5", maxTokens: new(1000), effort: "high", ceiling: 1000,
			wantTokens: 1000,
		},
		{
			name: "adaptive thinking has no budget: it stays under the clamped limit", model: "claude-haiku-5-5", maxTokens: new(1000), effort: "low", ceiling: 1000,
			wantTokens: 1000, wantThinking: &anthropicThinking{Type: "adaptive"}, wantEffort: "low",
		},
		{
			name: "the default limit is clamped under adaptive thinking", model: "claude-opus-5", effort: "high", ceiling: 1000,
			wantTokens: 1000, wantThinking: &anthropicThinking{Type: "adaptive"}, wantEffort: "high",
		},
		{
			name: "a model that always thinks keeps the least effort it accepts", model: "claude-fable-5-2", effort: "none", ceiling: 1000,
			wantTokens: 1000, wantThinking: &anthropicThinking{Type: "adaptive"}, wantEffort: "low",
		},
		{
			name: "thinking turned off on a model that thinks by default stays off", model: "claude-opus-5", effort: "none", ceiling: 1000,
			wantTokens: 1000, wantThinking: &anthropicThinking{Type: "disabled"},
		},
		{name: "a model that thinks by default gets no thinking block it did not ask for", model: "claude-opus-5", ceiling: 1000, wantTokens: 1000},
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
			if tt.wantEffort == "" {
				assert.Nil(t, translated.OutputConfig)
				return
			}
			require.NotNil(t, translated.OutputConfig)
			assert.Equal(t, tt.wantEffort, translated.OutputConfig.Effort)
		})
	}
}

// Leaving a budget's thinking block out turns thinking off only on a model
// that does not think unless asked. Every model that thinks on its own takes
// adaptive thinking, which has no budget for withinRequestCeilings to drop.
func TestModelsThatThinkOnTheirOwnTakeNoBudget(t *testing.T) {
	for _, model := range slices.Concat(defaultThinkingPrefixes, alwaysThinkingPrefixes) {
		assert.True(t, isAdaptiveThinkingModel(model), model)
	}
}

// The Messages ingress keeps its own copies of this adapter's budget table:
// it holds a native thinking budget to the one sent here for the ceiling
// level, and reads a budget back as the level it stands for. They have to
// agree with reasoningEffortToBudgetTokens for every level, or a ceiling
// would allow a budget the adapter never sends.
func TestMessagesBudgetsAgreeWithTheAdapter(t *testing.T) {
	messages := func(budget int) *anthropicapi.MessagesRequest {
		req, err := anthropicapi.DecodeMessagesRequest(fmt.Appendf(nil,
			`{"model":"claude-sonnet-4-5","max_tokens":200000,"thinking":{"type":"enabled","budget_tokens":%d},"messages":[{"role":"user","content":"hi"}]}`, budget))
		require.NoError(t, err)
		return req
	}
	for _, level := range requestparams.ReasoningEffortLevels() {
		if level == "none" {
			continue // allows no budget at all
		}
		budget := reasoningEffortToBudgetTokens(level)

		_, lowered := anthropicapi.WithinCeilings(messages(budget), level, 0, false)
		assert.False(t, lowered, "the %s budget is within a %s ceiling", level, level)
		above, lowered := anthropicapi.WithinCeilings(messages(budget+1), level, 0, false)
		require.True(t, lowered, level)
		assert.Equal(t, budget, above.Thinking.BudgetTokens, level)
	}
	for _, level := range []string{"low", "medium", "high"} {
		chat, err := anthropicapi.ToChatRequest(messages(reasoningEffortToBudgetTokens(level)))
		require.NoError(t, err)
		require.NotNil(t, chat.Reasoning)
		assert.Equal(t, level, chat.Reasoning.Effort)
	}
}
