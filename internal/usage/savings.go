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
	effective := pricingForEndpoint(pricing.AtTime(entry.Timestamp), entry.Endpoint)
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
// Prefer the request's observed blended input rate: its token-attributable
// input cost divided by the provider's full input parts (uncached + cache
// reads + cache writes).
// This makes cache-heavy traffic value removed tokens at the rate mix the
// request actually experienced instead of assuming every removed token was
// uncached. Fixed input charges retained by the rewrite are excluded. It
// remains a gross estimate: cache hits or misses caused by the rewrite itself
// are not modeled.
//
// When the observed rate is unavailable, fall back to the static-pricing
// counterfactual used by older entries. The counterfactual also takes
// precedence when the hypothetical uncompressed request crosses an input
// pricing tier, preserving the tier's non-linear re-rating. Returns nil when
// neither source can cost the input side.
func rewriteCostSaved(
	inputTokens, outputTokens int,
	rawData map[string]any,
	provider string,
	requestInputCost *float64,
	effectivePricing *core.ModelPricing,
	tokensSaved int,
) *float64 {
	if tokensSaved <= 0 {
		return nil
	}

	if rewriteCrossesInputPricingTier(effectivePricing, inputTokens, tokensSaved) {
		return rewriteCostSavedFromPricing(inputTokens, outputTokens, rawData, provider, effectivePricing, tokensSaved)
	}

	uncached, cached, cacheWrite := EntryInputSegments(UsageLogEntry{
		InputTokens: inputTokens,
		Provider:    provider,
		RawData:     rawData,
	})
	inputParts := uncached + cached + cacheWrite
	if requestInputCost != nil && isFiniteCost(*requestInputCost) && *requestInputCost >= 0 && inputParts > 0 {
		if tokenInputCost, ok := rewriteTokenInputCost(*requestInputCost, rawData, provider, effectivePricing); ok {
			saved := tokenInputCost * float64(tokensSaved) / float64(inputParts)
			return &saved
		}
	}

	return rewriteCostSavedFromPricing(inputTokens, outputTokens, rawData, provider, effectivePricing, tokensSaved)
}

// rewriteTokenInputCost removes unchanged non-token input charges before an
// observed request cost is spread across prompt-token parts. If raw usage
// identifies such a charge but its rate is unavailable, the split cannot be
// made safely and the caller must use another costing method.
func rewriteTokenInputCost(requestInputCost float64, rawData map[string]any, provider string, pricing *core.ModelPricing) (float64, bool) {
	fixedCost := 0.0

	if isXAIProvider(provider) {
		if images := extractInt(rawData, "image_tokens"); images > 0 {
			if pricing == nil || pricing.InputPerImage == nil {
				return 0, false
			}
			fixedCost += float64(images) * *pricing.InputPerImage
		}
	}

	if chars := extractInt(rawData, rawKeyInputCharacters); chars > 0 {
		if pricing == nil || pricing.PerCharacterInput == nil {
			return 0, false
		}
	}

	if seconds, ok := extractFloat(rawData, rawKeyAudioSeconds); ok && seconds > 0 {
		if pricing == nil || pricing.PerSecondInput == nil {
			return 0, false
		}
	}
	if pricing != nil {
		// Reuse the canonical calculator for both per-character and
		// per-second input charges so savings cannot drift from usage costs.
		audioInputCost, _, _ := applyAudioUnitCosts(rawData, pricing)
		fixedCost += audioInputCost
	}

	tokenInputCost := requestInputCost - fixedCost
	if !isFiniteCost(tokenInputCost) || tokenInputCost < -1e-12 {
		return 0, false
	}
	if tokenInputCost < 0 {
		tokenInputCost = 0
	}
	return tokenInputCost, true
}

func rewriteCrossesInputPricingTier(pricing *core.ModelPricing, inputTokens, tokensSaved int) bool {
	if pricing == nil || tokensSaved <= 0 || len(pricing.Tiers) == 0 {
		return false
	}
	forwarded := pricingForTokenCount(pricing, inputTokens)
	asSent := pricingForTokenCount(pricing, inputTokens+tokensSaved)
	return !samePrice(forwarded.InputPerMtok, asSent.InputPerMtok)
}

func samePrice(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func rewriteCostSavedFromPricing(
	inputTokens, outputTokens int,
	rawData map[string]any,
	provider string,
	effectivePricing *core.ModelPricing,
	tokensSaved int,
) *float64 {
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
