package groq

import (
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
)

// adaptChatRequest maps GoModel's nested reasoning shape (set by the Messages
// API's thinking and by clients sending reasoning.effort) onto Groq's flat
// reasoning_effort. Groq rejects "reasoning" outright and accepts
// reasoning_effort only on reasoning models, with per-family values.
func adaptChatRequest(req *core.ChatRequest) (*core.ChatRequest, error) {
	if req == nil || req.Reasoning == nil {
		return req, nil
	}
	effort := reasoningEffort(req.Model, req.Reasoning.Effort)
	if effort == "" {
		return providers.DropReasoning(req), nil
	}
	return providers.AdaptReasoningEffortRequest(req, effort)
}

// reasoningEffort returns the reasoning_effort value the model accepts, or ""
// when the model rejects the field: gpt-oss takes low/medium/high, qwen3
// takes none/default, and other models take nothing.
func reasoningEffort(model, effort string) string {
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "" {
		return ""
	}
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "gpt-oss"):
		if effort == "xhigh" || effort == "max" {
			return "high"
		}
		return effort
	case strings.Contains(m, "qwen3"):
		return "default"
	default:
		return ""
	}
}
