package gemini

import (
	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// foldCompatThinkingTokens brings usage from Google's OpenAI-compatible
// endpoint to the convention the native path and OpenAI use: thinking tokens
// are part of completion_tokens and itemised as reasoning tokens. The endpoint
// counts them in total_tokens only, so the part of the total that prompt and
// completion tokens do not explain is thinking, which Google bills as output.
//
// A response that itemises reasoning tokens bounds what is moved, so a total
// inflated by something else is never billed as thinking; without the item the
// whole excess is taken as thinking, the only source observed on this
// endpoint. A total that prompt and completion tokens already explain is left
// alone, so applying this twice changes nothing. It reports whether usage
// changed.
func foldCompatThinkingTokens(usage *core.Usage) bool {
	excess := usage.TotalTokens - usage.PromptTokens - usage.CompletionTokens
	if excess <= 0 {
		return false
	}
	thinking := excess
	itemised := compatReasoningTokens(usage)
	if itemised > 0 {
		thinking = min(excess, itemised)
	}
	usage.CompletionTokens += thinking

	var details core.CompletionTokensDetails
	if usage.CompletionTokensDetails != nil {
		details = *usage.CompletionTokensDetails
	}
	details.ReasoningTokens = max(itemised, thinking)
	usage.CompletionTokensDetails = &details
	return true
}

// compatReasoningTokens returns the reasoning tokens a response itemises, in
// the OpenAI completion_tokens_details member or as a top-level usage member.
func compatReasoningTokens(usage *core.Usage) int {
	if details := usage.CompletionTokensDetails; details != nil && details.ReasoningTokens > 0 {
		return details.ReasoningTokens
	}
	if count, ok := usage.RawUsage["reasoning_tokens"].(float64); ok && count > 0 {
		return int(count)
	}
	return 0
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
	if err := json.Unmarshal(encoded, &usage); err != nil || !foldCompatThinkingTokens(&usage) {
		return false
	}
	chunk["usage"] = usage
	return true
}
