package anthropicapi

import (
	"strings"

	"github.com/enterpilot/gomodel/internal/requestparams"
)

// The smallest budget_tokens Anthropic accepts, and the room for the answer
// kept next to a budget that has to fit under max_tokens. Both mirror the
// anthropic provider, which fits a translated request the same way.
const (
	minThinkingBudget  = 1024
	thinkingAnswerRoom = 1024
)

// adaptiveEffort is the level thinkingToReasoning gives adaptive thinking
// that names no effort.
const adaptiveEffort = "medium"

// WithinCeilings returns req lowered to its caller's ceilings, and whether
// that changed it: max_tokens above the token ceiling becomes the ceiling,
// output_config.effort above the effort ceiling becomes the ceiling, and a
// thinking budget is lowered to the budget the gateway sends for the ceiling
// level, then to what fits under a lowered max_tokens. Thinking is turned
// off when the ceiling allows none or no valid budget fits. Only MaxTokens,
// Thinking and OutputConfig differ in the result, which is req itself when
// nothing does. A zero ceiling bounds nothing.
//
// It is for a request forwarded to Anthropic in its own dialect; one the
// translated pipeline serves is lowered there.
func WithinCeilings(req *MessagesRequest, effortCeiling string, tokenCeiling int) (*MessagesRequest, bool) {
	if req == nil {
		return req, false
	}
	maxTokens, thinking, effort := req.MaxTokens, req.Thinking, ""
	if req.OutputConfig != nil {
		effort = req.OutputConfig.Effort
	}
	if tokenCeiling > 0 && maxTokens > tokenCeiling {
		maxTokens = tokenCeiling
	}
	if strings.TrimSpace(effortCeiling) != "" {
		thinking, effort = effortWithinCeiling(thinking, effort, effortCeiling)
	}
	if maxTokens < req.MaxTokens && thinking != nil && thinking.Type == "enabled" && thinking.BudgetTokens >= maxTokens {
		budget := max(maxTokens-thinkingAnswerRoom, minThinkingBudget)
		if budget >= maxTokens {
			thinking = &Thinking{Type: "disabled"}
		} else {
			thinking = &Thinking{Type: "enabled", BudgetTokens: budget}
		}
	}

	lowered := *req
	changed := maxTokens != req.MaxTokens
	lowered.MaxTokens = maxTokens
	if thinking != req.Thinking {
		lowered.Thinking, changed = thinking, true
	}
	if effort != "" && (req.OutputConfig == nil || effort != req.OutputConfig.Effort) {
		config := OutputConfig{Effort: effort}
		if req.OutputConfig != nil {
			config.Format = req.OutputConfig.Format
		}
		lowered.OutputConfig, changed = &config, true
	}
	if !changed {
		return req, false
	}
	return &lowered, true
}

// effortWithinCeiling lowers a thinking config and an output_config effort
// to ceiling. thinking is returned as it is, or replaced, never edited.
func effortWithinCeiling(thinking *Thinking, effort, ceiling string) (*Thinking, string) {
	limit := requestparams.EffortRank(ceiling)
	exceeds := func(level string) bool {
		rank := requestparams.EffortRank(level)
		return rank < 0 || rank > limit
	}
	budget := effortBudget(ceiling)
	thinks := thinking != nil && thinking.Type != "" && thinking.Type != "disabled"
	if thinks && budget == 0 {
		thinking, thinks = &Thinking{Type: "disabled"}, false
	}
	switch {
	case strings.TrimSpace(effort) != "":
		if exceeds(effort) {
			effort = messagesEffort(ceiling)
		}
	case thinks && thinking.Type == "adaptive" && exceeds(adaptiveEffort):
		effort = messagesEffort(ceiling)
	}
	if thinks && thinking.Type == "enabled" && thinking.BudgetTokens > budget {
		thinking = &Thinking{Type: "enabled", BudgetTokens: budget}
	}
	return thinking, effort
}

// messagesEffort is the output_config.effort that stands for a ceiling
// level. Anthropic's scale starts at "low", so the levels below it map there.
func messagesEffort(ceiling string) string {
	switch level := strings.ToLower(strings.TrimSpace(ceiling)); level {
	case "none", "minimal":
		return "low"
	default:
		return level
	}
}
