package usage

import "github.com/enterpilot/gomodel/internal/core"

// ApplyRewriteSavings folds a request-rewrite savings estimate into a usage
// entry: RewriteTokensSaved always, and RewriteCostSaved when the request's
// observed input cost or model pricing allows costing the removed tokens.
func ApplyRewriteSavings(entry *UsageEntry, tokensSaved int, pricing *core.ModelPricing) {
	if entry == nil || tokensSaved <= 0 {
		return
	}
	entry.RewriteTokensSaved = tokensSaved
	effective := pricingForEndpoint(pricing, entry.Endpoint)
	entry.RewriteCostSaved = rewriteCostSaved(
		entry.InputTokens,
		entry.OutputTokens,
		entry.RawData,
		entry.Provider,
		entry.InputCost,
		effective,
		tokensSaved,
	)
}

// rewriteCostSaved estimates the gross input cost avoided by a rewrite.
//
// Prefer the request's observed blended input rate: actual input cost divided
// by the provider's full input parts (uncached + cache reads + cache writes).
// This makes cache-heavy traffic value removed tokens at the rate mix the
// request actually experienced instead of assuming every removed token was
// uncached. It remains a gross estimate: cache hits or misses caused by the
// rewrite itself are not modeled.
//
// When the observed rate is unavailable, fall back to the static-pricing
// counterfactual used by older entries. Returns nil when neither source can
// cost the input side.
func rewriteCostSaved(
	inputTokens, outputTokens int,
	rawData map[string]any,
	provider string,
	actualInputCost *float64,
	effectivePricing *core.ModelPricing,
	tokensSaved int,
) *float64 {
	if tokensSaved <= 0 {
		return nil
	}

	uncached, cached, cacheWrite := EntryInputSegments(UsageLogEntry{
		InputTokens: inputTokens,
		Provider:    provider,
		RawData:     rawData,
	})
	inputParts := uncached + cached + cacheWrite
	if actualInputCost != nil && isFiniteCost(*actualInputCost) && *actualInputCost >= 0 && inputParts > 0 {
		saved := *actualInputCost * float64(tokensSaved) / float64(inputParts)
		return &saved
	}

	if effectivePricing == nil {
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
