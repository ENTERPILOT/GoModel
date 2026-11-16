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
		// GoModel's nested reasoning.effort is the canonical field: as with
		// every other provider using AdaptReasoningEffortRequest, it wins over
		// a flat reasoning_effort the client sent alongside it.
		if req.Reasoning != nil && strings.TrimSpace(req.Reasoning.Effort) != "" {
			return providers.AdaptReasoningEffortRequest(req, normalizeReasoningEffort(req.Reasoning.Effort))
		}
		// Nothing canonical to map: a client that speaks the flat wire shape
		// asked for reasoning too, so the default must not overwrite it.
		if defaultEffort == "" || req.ExtraFields.Lookup("reasoning_effort") != nil {
			return req, nil
		}
		return providers.AdaptReasoningEffortRequest(req, defaultEffort)
	}
}

// adaptResponsesRequest applies to a native Responses request the policy
// adaptChatRequest applies to chat: the effort is mapped onto the low/high/max
// set OpenCode Zen accepts, and defaultEffort fills in one that carries none.
// The Responses dialect already speaks the nested reasoning object, so only the
// effort level and the injected default differ from what the client sent.
func adaptResponsesRequest(defaultEffort string) func(*core.ResponsesRequest) *core.ResponsesRequest {
	return func(req *core.ResponsesRequest) *core.ResponsesRequest {
		if req == nil {
			return nil
		}
		// A flat reasoning_effort in the provider extras is the client asking
		// for reasoning in the chat dialect: it must not be overwritten, the
		// same precedence adaptChatRequest applies.
		flatEffort := req.ExtraFields.Lookup("reasoning_effort") != nil
		if req.Reasoning == nil || strings.TrimSpace(req.Reasoning.Effort) == "" {
			if defaultEffort == "" || flatEffort {
				return req
			}
			reasoning := core.Reasoning{}
			if req.Reasoning != nil {
				reasoning = *req.Reasoning
			}
			reasoning.Effort = defaultEffort
			adapted := *req
			adapted.Reasoning = &reasoning
			return &adapted
		}
		effort := normalizeReasoningEffort(req.Reasoning.Effort)
		if effort == req.Reasoning.Effort {
			return req
		}
		adapted := *req
		reasoning := *req.Reasoning
		reasoning.Effort = effort
		adapted.Reasoning = &reasoning
		return &adapted
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
