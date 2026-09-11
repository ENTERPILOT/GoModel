package groq

import (
	"strconv"
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
// when the field should be left out:
//   - gpt-oss: low, medium, high (it cannot turn reasoning off).
//   - qwen3.8 and later: none, low, medium, high, default.
//   - earlier qwen3: none or default (on/off only).
//   - other models reject the field.
func reasoningEffort(model, effort string) string {
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "" {
		return ""
	}
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "gpt-oss"):
		return leveledEffort(effort, false)
	case strings.Contains(m, "qwen3"):
		if qwenMinor(m) >= 8 {
			return leveledEffort(effort, true)
		}
		if effort == "none" {
			return "none"
		}
		return "default"
	default:
		return ""
	}
}

// leveledEffort maps an effort onto low/medium/high, plus none and default
// when the model accepts them. An unknown value is left out so the upstream
// default applies.
func leveledEffort(effort string, acceptsNone bool) string {
	switch effort {
	case "low", "medium", "high":
		return effort
	case "xhigh", "max":
		return "high"
	case "minimal":
		return "low"
	case "none", "default":
		if acceptsNone {
			return effort
		}
		if effort == "none" {
			return "low"
		}
		return ""
	default:
		return ""
	}
}

// qwenMinor returns N of a "qwen3.N" model id, or 0 when there is none.
func qwenMinor(model string) int {
	_, rest, ok := strings.Cut(model, "qwen3.")
	if !ok {
		return 0
	}
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	n, _ := strconv.Atoi(rest[:end])
	return n
}
