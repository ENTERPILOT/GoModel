package usage

import "github.com/enterpilot/gomodel/internal/core"

const endpointDecisions = "/v1/decisions"

// pricingForDecisionsEndpoint prices a Decisions call: only input tokens are
// billed, every one at the input rate. OpenAI's Decisions API has "no
// cache-read, cache-write, or output-token charges", and TypeSafe does not
// charge for System One output either. Long-context tiers still set the
// input rate. A model priced for chat (gpt-6-luna) would otherwise bill the
// answer as chat output and discount cached input.
func pricingForDecisionsEndpoint(pricing *core.ModelPricing) *core.ModelPricing {
	if pricing == nil {
		return nil
	}
	free := 0.0
	effective := *pricing
	effective.OutputPerMtok = &free
	effective.CachedInputPerMtok = nil
	effective.CacheWritePerMtok = nil
	effective.CacheWrite1hPerMtok = nil
	effective.ReasoningOutputPerMtok = nil
	if len(pricing.Tiers) > 0 {
		effective.Tiers = make([]core.ModelPricingTier, len(pricing.Tiers))
		for i, tier := range pricing.Tiers {
			tier.OutputPerMtok = &free
			tier.CachedInputPerMtok = nil
			effective.Tiers[i] = tier
		}
	}
	return &effective
}
