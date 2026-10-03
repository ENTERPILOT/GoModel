package openai

import (
	"slices"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
)

// effortOrder ranks the reasoning effort levels from least to most reasoning.
var effortOrder = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// chatEffortLevels returns the reasoning_effort values the model accepts on
// Chat Completions, in effortOrder, or nil for models whose levels are not
// known (which then receive the requested effort unchanged). OpenAI rejects
// any other value with a 400; the sets were verified against the API.
func chatEffortLevels(model string) []string {
	if isOSeriesModel(strings.ToLower(strings.TrimSpace(model))) {
		return []string{"low", "medium", "high"}
	}
	major, minor, ok := gptVersion(model)
	switch {
	case !ok || major < 5:
		return nil
	case major == 5 && minor == 0:
		return []string{"minimal", "low", "medium", "high"}
	case major == 5 && minor == 1:
		return []string{"none", "low", "medium", "high"}
	case rejectsNoReasoning(model):
		return []string{"low", "medium", "high", "xhigh"}
	default:
		return []string{"none", "low", "medium", "high", "xhigh"}
	}
}

// supportedEffort maps a reasoning effort onto the closest level the model
// accepts: a level below what it supports rounds up ("minimal" becomes "low"
// on GPT-5.1+), a level above rounds down ("max" becomes "xhigh"). Unknown
// efforts and models pass through.
func supportedEffort(model, effort string) string {
	levels := chatEffortLevels(model)
	rank := slices.Index(effortOrder, effort)
	if levels == nil || rank < 0 || slices.Contains(levels, effort) {
		return effort
	}
	for _, level := range levels {
		if slices.Index(effortOrder, level) > rank {
			return level
		}
	}
	return levels[len(levels)-1]
}

// reasoningOff reports whether an effort turns reasoning off, the only case in
// which OpenAI reasoning models accept temperature and top_p.
func reasoningOff(effort string) bool {
	return effort == "none"
}

// flatEffort returns the reasoning_effort member of a request, or "".
func flatEffort(req *core.ChatRequest) string {
	var effort string
	if raw := req.ExtraFields.Lookup("reasoning_effort"); len(raw) > 0 && json.Unmarshal(raw, &effort) == nil {
		return strings.TrimSpace(effort)
	}
	return ""
}

// adaptFlatEffort applies supportedEffort to a reasoning_effort member the
// caller sent directly, leaving the request unchanged when it already fits.
func adaptFlatEffort(req *core.ChatRequest) (*core.ChatRequest, error) {
	effort := flatEffort(req)
	if effort == "" {
		return req, nil
	}
	if supported := supportedEffort(req.Model, effort); supported != effort {
		return providers.AdaptReasoningEffortRequest(req, supported)
	}
	return req, nil
}
