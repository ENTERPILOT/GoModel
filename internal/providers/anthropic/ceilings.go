package anthropic

import (
	"context"
	"log/slog"

	"github.com/enterpilot/gomodel/internal/core"
)

// minThinkingBudget is the smallest budget_tokens Anthropic accepts.
const minThinkingBudget = 1024

// thinkingAnswerRoom is the room for the answer kept next to a thinking
// budget, as applyReasoning keeps it when it raises max_tokens.
const thinkingAnswerRoom = 1024

// withinRequestCeilings keeps a translated request within the output token
// ceiling of the caller's policy. The gateway already lowered the client's
// limit, but the translation may raise it again: the default it injects when
// the client sent none, or the room applyReasoning adds above a thinking
// budget. Both are clamped to the ceiling, and a thinking budget that no
// longer fits under max_tokens shrinks to fit; when not even the minimum
// budget fits, thinking is turned off rather than the ceiling exceeded.
func withinRequestCeilings(ctx context.Context, req *anthropicRequest) {
	ceiling := core.RequestCeilingsFromContext(ctx).OutputTokens
	if req == nil || ceiling <= 0 {
		return
	}
	if req.MaxTokens > ceiling {
		req.MaxTokens = ceiling
	}
	if req.Thinking == nil || req.Thinking.Type != "enabled" || req.Thinking.BudgetTokens < req.MaxTokens {
		return
	}
	budget := max(req.MaxTokens-thinkingAnswerRoom, minThinkingBudget)
	if budget >= req.MaxTokens {
		slog.Info("thinking disabled: no thinking budget fits under the output token ceiling",
			"max_tokens", req.MaxTokens, "requested_budget", req.Thinking.BudgetTokens)
		req.Thinking = nil
		if thinksByDefault(req.Model) {
			req.Thinking = &anthropicThinking{Type: "disabled"}
		}
		return
	}
	slog.Info("thinking budget lowered to fit the output token ceiling",
		"max_tokens", req.MaxTokens, "requested_budget", req.Thinking.BudgetTokens, "budget", budget)
	req.Thinking.BudgetTokens = budget
}
