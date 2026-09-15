package zai

import (
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
)

// adaptChatRequest rewrites GoModel's common reasoning shape into Z.ai's chat
// extension. The Z.ai Chat Completions API takes reasoning_effort as a
// top-level string and has no "reasoning" object: forwarded as-is, the nested
// shape is silently ignored and GLM models keep their default effort ("max"),
// whatever the client asked for. The Responses API reaches Z.ai through chat
// translation, so this hook covers both.
func adaptChatRequest(req *core.ChatRequest) (*core.ChatRequest, error) {
	if req == nil || req.Reasoning == nil {
		return req, nil
	}
	effort := strings.TrimSpace(req.Reasoning.Effort)
	if effort == "" {
		return providers.DropReasoning(req), nil
	}
	return providers.AdaptReasoningEffortRequest(req, normalizeReasoningEffort(req.Model, effort))
}

// normalizeReasoningEffort maps GoModel effort levels onto the levels Z.ai
// accepts. GLM-5.3 and GLM-5.3-Flash always think and document only "low",
// "high", and "max" (glm-5.3 answers 400 "This model always engages in
// thinking and cannot be disabled; please use low, high, or max" for the
// rest), so "none" and "minimal" become "low", "medium" becomes "high", and
// "xhigh" becomes "max" — the same mapping Z.ai applies itself to GLM-5.2.
// Other models receive the level unchanged, and values outside GoModel's
// vocabulary pass through for the upstream to judge.
func normalizeReasoningEffort(model, effort string) string {
	normalized := strings.ToLower(strings.TrimSpace(effort))
	if !isGLM53(model) {
		return normalized
	}
	switch normalized {
	case "none", "minimal":
		return "low"
	case "medium":
		return "high"
	case "xhigh":
		return "max"
	default:
		return normalized
	}
}

// isGLM53 reports whether the model belongs to the GLM-5.3 family (glm-5.3,
// glm-5.3-flash), whose effort vocabulary is restricted to low/high/max.
func isGLM53(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return m == "glm-5.3" || strings.HasPrefix(m, "glm-5.3-")
}
