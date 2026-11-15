package usage

// reasoningRawDataKeys are the RawData names providers report reasoning
// ("thinking") output tokens under.
var reasoningRawDataKeys = map[string]struct{}{
	"reasoning_tokens":            {},
	"completion_reasoning_tokens": {},
	"completion_thinking_tokens":  {},
	"thought_tokens":              {},
	"thoughts_token_count":        {},
}

// EntryReasoningTokens returns the reasoning output tokens a usage entry
// reports and whether its OutputTokens already contain them. They do for
// every usage object but that of xAI's chat completions (see
// reasoningOnTopOfOutput), the rule the entry was costed by.
func EntryReasoningTokens(entry UsageLogEntry) (tokens int64, includedInOutput bool) {
	reasoning := reasoningTokens(entry.Provider, entry.RawData)
	onTop := reasoningOnTopOfOutput(entry.Provider, entry.InputTokens, entry.OutputTokens, entry.TotalTokens, reasoning)
	return int64(reasoning), !onTop
}

// reasoningTokens reads the reasoning count from the names the provider's
// pricing mappings know; aliases of one count are not added up.
func reasoningTokens(providerType string, rawData map[string]any) int {
	tokens := 0
	for _, m := range tokenCostMappingsForProvider(providerType) {
		if _, ok := reasoningRawDataKeys[m.rawDataKey]; ok {
			tokens = max(tokens, extractInt(rawData, m.rawDataKey))
		}
	}
	return tokens
}

// reasoningOnTopOfOutput reports whether a usage object counts its reasoning
// tokens beside its output tokens instead of inside them. Only xAI's chat
// completions do: completion_tokens leaves reasoning out, and total_tokens is
// prompt + completion + reasoning. xAI's Responses API follows OpenAI:
// output_tokens contains reasoning, and total_tokens is input + output.
//
// Both report the count under the same name, so the total decides: it follows
// the usage object whichever endpoint the caller used. A usage object whose
// total has no room for the reasoning tokens, or that has no total, is read as
// containing them, which never counts a token twice.
func reasoningOnTopOfOutput(providerType string, inputTokens, outputTokens, totalTokens, reasoning int) bool {
	return isXAIProvider(providerType) && reasoning > 0 && totalTokens >= inputTokens+outputTokens+reasoning
}

// completeOutputTokens returns the output tokens of a usage object with its
// reasoning tokens counted in, the count the output rate is charged on.
func completeOutputTokens(providerType string, inputTokens, outputTokens, totalTokens int, rawData map[string]any) int {
	reasoning := reasoningTokens(providerType, rawData)
	if reasoningOnTopOfOutput(providerType, inputTokens, outputTokens, totalTokens, reasoning) {
		return outputTokens + reasoning
	}
	return outputTokens
}
