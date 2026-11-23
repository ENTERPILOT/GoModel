package cohere

import (
	"strconv"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// thinkingAnswerRoom is what a thinking budget leaves of max_tokens for the
// answer. Cohere refuses a budget above max_tokens and recommends leaving at
// least 1K tokens for the response.
const thinkingAnswerRoom = 1024

// thinkingForEffort returns Cohere's thinking control for a reasoning effort
// of the gateway's common scale, or nil where the model's default stays.
// Cohere reads no effort: its reasoning models think by default, without a
// budget, unless thinking says otherwise, so an effort the adapter dropped
// would run at full effort.
//
// "none" turns thinking off; every Cohere model takes that. A level becomes
// a token budget, on a reasoning model only, since the others refuse
// thinking that is enabled: the budgets are the ones the Gemini adapter
// sends for the same levels. "max", and a value off the scale, keep the
// default, which is unlimited thinking. A budget leaves thinkingAnswerRoom of
// maxTokens, and thinking is turned off when no budget fits.
func thinkingForEffort(model string, reasoning *core.Reasoning, maxTokens *int) json.RawMessage {
	if reasoning == nil {
		return nil
	}
	budget := 0
	switch strings.ToLower(strings.TrimSpace(reasoning.Effort)) {
	case "none":
		return json.RawMessage(`{"type":"disabled"}`)
	case "minimal", "low":
		budget = 1024
	case "medium":
		budget = 8192
	case "high", "xhigh":
		budget = 24576
	default:
		return nil
	}
	if !isReasoningModel(model) {
		return nil
	}
	if maxTokens != nil {
		if budget = min(budget, *maxTokens-thinkingAnswerRoom); budget < 1 {
			return json.RawMessage(`{"type":"disabled"}`)
		}
	}
	return json.RawMessage(`{"type":"enabled","token_budget":` + strconv.Itoa(budget) + `}`)
}

// isReasoningModel reports whether model is one of Cohere's reasoning models
// (command-a-reasoning-08-2025), by the name Cohere gives them.
func isReasoningModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "reasoning")
}
