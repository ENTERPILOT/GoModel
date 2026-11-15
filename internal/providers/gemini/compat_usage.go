package gemini

import (
	"maps"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// compatFoldedKey is the usage member that marks completion_tokens as
// containing the reasoning tokens. It is GoModel's own name for that count,
// the one the native adapter reports too, and Google never sends it, so a
// usage object carrying it has already been through an adapter.
const compatFoldedKey = "completion_reasoning_tokens"

// compatReasoningKey is the top-level usage member the endpoint may itemise
// reasoning tokens under, beside completion_tokens_details.reasoning_tokens.
const compatReasoningKey = "reasoning_tokens"

// compatChatResponse decodes a chat completion from Google's OpenAI-compatible
// endpoint and folds its thinking tokens into the completion tokens.
type compatChatResponse struct {
	core.ChatResponse
}

func (r *compatChatResponse) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, &r.ChatResponse); err != nil {
		return err
	}
	var envelope struct {
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	foldCompatThinkingTokens(&r.Usage, compatItemisedReasoning(envelope.Usage))
	return nil
}

// foldCompatThinkingTokens brings usage from Google's OpenAI-compatible
// endpoint to the convention the native path and OpenAI use: thinking tokens
// are part of completion_tokens and itemised as reasoning tokens. The endpoint
// counts them in total_tokens only, so they are found in the part of the total
// that prompt and completion tokens do not explain, and Google bills them as
// output.
//
// itemised is the reasoning count the response reports, nil when it reports
// none. It caps what is moved, so a total inflated by something else is not
// billed as thinking and an explicit zero moves nothing. Without it the whole
// excess is taken as thinking, the only source observed on this endpoint.
//
// The reasoning count reported afterwards is the itemised one, or what was
// moved when none was itemised, and never more than the completion tokens: an
// itemised count above them is cut down to them in every member that carries
// it, also when nothing is moved, so no reasoning token is priced that is not
// also an output token.
//
// A folded usage is marked with compatFoldedKey and nothing is moved into it
// again, so folding twice changes nothing. It reports whether usage changed.
func foldCompatThinkingTokens(usage *core.Usage, itemised *int) bool {
	_, folded := usage.RawUsage[compatFoldedKey]
	thinking := usage.TotalTokens - usage.PromptTokens - usage.CompletionTokens
	if itemised != nil {
		thinking = min(thinking, *itemised)
	}
	if folded || thinking <= 0 {
		if itemised == nil || *itemised <= usage.CompletionTokens {
			return false
		}
		setCompatReasoningTokens(usage, max(usage.CompletionTokens, 0), false)
		return true
	}
	usage.CompletionTokens += thinking
	reasoning := thinking
	if itemised != nil {
		reasoning = min(*itemised, usage.CompletionTokens)
	}
	setCompatReasoningTokens(usage, reasoning, true)
	return true
}

// setCompatReasoningTokens writes the reasoning count to every usage member
// that reports it. mark adds the members of a folded usage: the OpenAI detail
// and compatFoldedKey.
func setCompatReasoningTokens(usage *core.Usage, reasoning int, mark bool) {
	if mark || usage.CompletionTokensDetails != nil {
		var details core.CompletionTokensDetails
		if usage.CompletionTokensDetails != nil {
			details = *usage.CompletionTokensDetails
		}
		details.ReasoningTokens = reasoning
		usage.CompletionTokensDetails = &details
	}

	raw := maps.Clone(usage.RawUsage)
	if raw == nil {
		raw = make(map[string]any, 1)
	}
	if mark {
		raw[compatFoldedKey] = reasoning
	}
	for _, key := range []string{compatFoldedKey, compatReasoningKey} {
		if _, reported := raw[key]; reported {
			raw[key] = reasoning
		}
	}
	if len(raw) > 0 {
		usage.RawUsage = raw
	}
}

// compatItemisedReasoning returns the reasoning tokens a usage object
// itemises, in the OpenAI completion_tokens_details member or as a top-level
// member, and nil when it has neither. An explicit zero is a count.
func compatItemisedReasoning(usage []byte) *int {
	var probe struct {
		ReasoningTokens *float64 `json:"reasoning_tokens"`
		Details         struct {
			ReasoningTokens *float64 `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	}
	if len(usage) == 0 || json.Unmarshal(usage, &probe) != nil {
		return nil
	}
	reported := probe.Details.ReasoningTokens
	if reported == nil {
		reported = probe.ReasoningTokens
	}
	if reported == nil {
		return nil
	}
	count := int(*reported)
	return &count
}

// foldCompatChunkUsage applies foldCompatThinkingTokens to the usage member of
// a decoded stream chunk and reports whether the chunk changed.
func foldCompatChunkUsage(chunk map[string]any) bool {
	raw, ok := chunk["usage"].(map[string]any)
	if !ok {
		return false
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return false
	}
	var usage core.Usage
	if err := json.Unmarshal(encoded, &usage); err != nil || !foldCompatThinkingTokens(&usage, compatItemisedReasoning(encoded)) {
		return false
	}
	chunk["usage"] = usage
	return true
}
