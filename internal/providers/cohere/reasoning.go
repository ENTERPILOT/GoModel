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
func thinkingForEffort(model, effort string, maxTokens *int) json.RawMessage {
	budget := 0
	switch strings.ToLower(strings.TrimSpace(effort)) {
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

// requestedEffort returns the reasoning effort a request asks for: the
// reasoning.effort object, or else the flat Chat Completions reasoning_effort
// that rides in the extra fields.
func requestedEffort(req *core.ChatRequest) string {
	if req.Reasoning != nil && strings.TrimSpace(req.Reasoning.Effort) != "" {
		return req.Reasoning.Effort
	}
	var effort string
	if raw := req.ExtraFields.Lookup("reasoning_effort"); len(raw) > 0 && json.Unmarshal(raw, &effort) == nil {
		return effort
	}
	return ""
}

// thinkingModels names the Cohere model families that take thinking that is
// enabled, and think by default: command-a-reasoning-08-2025 and
// command-a-plus-05-2026. Cohere answers 400 for it on every other model
// ("`thinking` parameter is not supported with the specified model").
var thinkingModels = []string{"command-a-reasoning", "command-a-plus"}

// isReasoningModel reports whether model is one of thinkingModels.
func isReasoningModel(model string) bool {
	model = strings.ToLower(model)
	for _, family := range thinkingModels {
		if strings.Contains(model, family) {
			return true
		}
	}
	return false
}
