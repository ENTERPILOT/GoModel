package opencodego

import (
	"os"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
)

// defaultReasoningEffortEnvVar names the override for the reasoning effort
// injected when a client sends no reasoning parameter at all.
const defaultReasoningEffortEnvVar = "OPENCODE_GO_DEFAULT_REASONING_EFFORT"

// defaultReasoningEffort is injected when the client omits reasoning. Some
// OpenCode Zen models always think and reject a request that leaves the
// parameter out ("This model always engages in thinking and cannot be
// disabled; please use low, high, or max"), so an absent parameter must not
// read as "thinking off". "low" is the cheapest level those models accept;
// raise it with OPENCODE_GO_DEFAULT_REASONING_EFFORT. Models that ignore the
// parameter are unaffected.
const defaultReasoningEffort = "low"

// adaptChatRequest returns the AdaptChatRequest hook for OpenCode Zen's
// /chat/completions dialect. It maps GoModel's nested reasoning shape onto the
// top-level "reasoning_effort" string the upstream documents, and fills in
// defaultEffort when the client asked for no reasoning at all. An empty
// defaultEffort disables injection.
func adaptChatRequest(defaultEffort string) func(*core.ChatRequest) (*core.ChatRequest, error) {
	return func(req *core.ChatRequest) (*core.ChatRequest, error) {
		if req == nil {
			return req, nil
		}
		if req.Reasoning != nil && strings.TrimSpace(req.Reasoning.Effort) != "" {
			return providers.AdaptReasoningEffortRequest(req, normalizeReasoningEffort(req.Reasoning.Effort))
		}
		// A client that already speaks the flat wire shape keeps full control.
		if defaultEffort == "" || req.ExtraFields.Lookup("reasoning_effort") != nil {
			return req, nil
		}
		return providers.AdaptReasoningEffortRequest(req, defaultEffort)
	}
}

// normalizeReasoningEffort maps GoModel's effort levels onto the low/high/max
// set OpenCode Zen accepts, downgrading the levels it does not know to their
// nearest supported equivalent. "none" becomes "low" because the models that
// enforce this cannot turn thinking off. Values outside GoModel's vocabulary
// pass through for the upstream to judge.
func normalizeReasoningEffort(effort string) string {
	normalized := strings.ToLower(strings.TrimSpace(effort))
	switch normalized {
	case "none", "minimal", "low", "medium":
		return "low"
	case "xhigh", "max":
		return "max"
	default:
		return normalized
	}
}

// loadDefaultReasoningEffort resolves the effort injected for requests without
// reasoning, honoring OPENCODE_GO_DEFAULT_REASONING_EFFORT. "none" and "off"
// disable injection for operators whose upstream models reject the parameter.
func loadDefaultReasoningEffort() string {
	override := strings.TrimSpace(os.Getenv(defaultReasoningEffortEnvVar))
	if override == "" {
		return defaultReasoningEffort
	}
	if strings.EqualFold(override, "none") || strings.EqualFold(override, "off") {
		return ""
	}
	return normalizeReasoningEffort(override)
}
