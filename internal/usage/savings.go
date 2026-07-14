package usage

import "github.com/enterpilot/gomodel/internal/core"

// ApplyRewriteSavings folds a request-rewrite savings estimate into a usage
// entry: RewriteTokensSaved always, and RewriteCostSaved when pricing allows
// costing the removed tokens.
func ApplyRewriteSavings(entry *UsageEntry, tokensSaved int, pricing *core.ModelPricing) {
	if entry == nil || tokensSaved <= 0 {
		return
	}
	entry.RewriteTokensSaved = tokensSaved
	effective := pricingForEndpoint(pricing, entry.Endpoint)
	entry.RewriteCostSaved = rewriteCostSaved(entry.InputTokens, entry.OutputTokens, entry.RawData, entry.Provider, effective, tokensSaved)
}

// rewriteCostSaved estimates the input cost the removed prompt tokens would
// have added: the input-cost delta between the request as the client sent it
// (input tokens + saved tokens) and the request as forwarded, both priced via
// CalculateGranularCost with the already endpoint-adjusted pricing. Going
// through the full granular calculation (rather than saved × base input rate)
// keeps tiered pricing honest: when the un-rewritten prompt would have landed
// in a higher tier, the delta includes the re-rating of the whole input.
// Returns nil when pricing cannot cost the input side.
func rewriteCostSaved(inputTokens, outputTokens int, rawData map[string]any, provider string, effectivePricing *core.ModelPricing, tokensSaved int) *float64 {
	if tokensSaved <= 0 || effectivePricing == nil {
		return nil
	}
	asSent := CalculateGranularCost(inputTokens+tokensSaved, outputTokens, rawData, provider, effectivePricing)
	asForwarded := CalculateGranularCost(inputTokens, outputTokens, rawData, provider, effectivePricing)
	if asSent.InputCost == nil || asForwarded.InputCost == nil {
		return nil
	}
	saved := *asSent.InputCost - *asForwarded.InputCost
	if saved < 0 {
		saved = 0
	}
	return &saved
}
