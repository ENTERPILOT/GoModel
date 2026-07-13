package usage

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

type recordingPricingResolver struct {
	model    string
	provider string
	pricing  *core.ModelPricing
}

func (r *recordingPricingResolver) ResolvePricing(model, providerType string) *core.ModelPricing {
	r.model = model
	r.provider = providerType
	return r.pricing
}

func TestRecalculateEntryCostsPrefersProviderNameForPricingLookup(t *testing.T) {
	inputRate := 2.0
	cachedRate := 0.5
	resolver := &recordingPricingResolver{
		pricing: &core.ModelPricing{
			InputPerMtok:       &inputRate,
			CachedInputPerMtok: &cachedRate,
		},
	}

	update := recalculateEntryCosts(recalculationEntry{
		ID:           "usage-1",
		Model:        "gpt-4o",
		Provider:     "openai",
		ProviderName: "primary-openai",
		InputTokens:  1_000_000,
		RawData: map[string]any{
			"cached_tokens": 500_000,
		},
	}, resolver)

	if resolver.model != "gpt-4o" || resolver.provider != "primary-openai" {
		t.Fatalf("ResolvePricing called with %q/%q, want gpt-4o/primary-openai", resolver.provider, resolver.model)
	}
	if update.InputCost == nil || *update.InputCost != 1.25 {
		t.Fatalf("InputCost = %v, want 1.25", update.InputCost)
	}
}
