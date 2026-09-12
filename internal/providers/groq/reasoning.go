package groq

import (
	"strconv"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
)

// maxStopSequences is the longest "stop" list Groq accepts; a longer one is
// rejected with "maximum number of items is 4".
const maxStopSequences = 4

// adaptChatRequest fits the canonical chat request to Groq: it truncates an
// over-long "stop" list, maps GoModel's nested reasoning shape (set by the
// Messages API's thinking and by clients sending reasoning.effort) onto Groq's
// flat reasoning_effort, and asks Groq to parse chain of thought out of the
// answer. Groq rejects "reasoning" outright and accepts reasoning_effort only
// on reasoning models, with per-family values.
func adaptChatRequest(req *core.ChatRequest) (*core.ChatRequest, error) {
	if req == nil {
		return req, nil
	}
	adapted, err := providers.CapStopSequences(req, maxStopSequences)
	if err != nil {
		return nil, err
	}
	adapted, err = adaptReasoningEffort(adapted)
	if err != nil {
		return nil, err
	}
	return adaptReasoningFormat(adapted)
}

func adaptReasoningEffort(req *core.ChatRequest) (*core.ChatRequest, error) {
	if req.Reasoning == nil {
		return req, nil
	}
	effort := reasoningEffort(req.Model, req.Reasoning.Effort)
	if effort == "" {
		return providers.DropReasoning(req), nil
	}
	return providers.AdaptReasoningEffortRequest(req, effort)
}

// adaptReasoningFormat defaults Groq's reasoning_format to "parsed" on the
// model families that accept it. Without it the Qwen models return their
// chain of thought as inline <think>...</think> text inside the answer, which
// an OpenAI-compatible client renders as the answer itself; "parsed" moves it
// to the separate reasoning field the gateway normalizes into
// reasoning_content (and into Messages API thinking blocks). A caller that
// sets reasoning_format explicitly keeps its own value.
func adaptReasoningFormat(req *core.ChatRequest) (*core.ChatRequest, error) {
	if !supportsReasoningFormat(req.Model) || req.ExtraFields.Lookup("reasoning_format") != nil {
		return req, nil
	}
	extra, err := core.MergeUnknownJSONFields(req.ExtraFields, map[string]json.RawMessage{
		"reasoning_format": json.RawMessage(`"parsed"`),
	})
	if err != nil {
		return nil, core.NewInvalidRequestError("failed to adapt reasoning request: "+err.Error(), err)
	}
	adapted := *req
	adapted.ExtraFields = extra
	return &adapted, nil
}

// supportsReasoningFormat reports whether the model accepts Groq's
// reasoning_format parameter. Only the reasoning families do: the compound
// systems and the plain chat models reject it with 400
// "`reasoning_format` is not supported with this model".
func supportsReasoningFormat(model string) bool {
	m := strings.ToLower(model)
	return strings.Contains(m, "gpt-oss") || strings.Contains(m, "qwen3")
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
