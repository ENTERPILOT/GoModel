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
// reports and whether its OutputTokens already contain them. Most providers
// count reasoning inside the completion tokens; xAI reports it on top. The
// answer comes from the pricing mappings, so it stays in step with how the
// entry was costed.
func EntryReasoningTokens(providerType string, rawData map[string]any) (tokens int64, includedInOutput bool) {
	for _, m := range tokenCostMappingsForProvider(providerType) {
		if _, ok := reasoningRawDataKeys[m.rawDataKey]; !ok {
			continue
		}
		if count := int64(extractInt(rawData, m.rawDataKey)); count > tokens {
			tokens, includedInOutput = count, m.includedInBase
		}
	}
	return tokens, includedInOutput
}
