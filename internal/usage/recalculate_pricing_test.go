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

func TestRecalculateEntryCostsPreservesMissingUsageCaveats(t *testing.T) {
	tokenPricing := &core.ModelPricing{InputPerMtok: new(0.3), OutputPerMtok: new(30.0)}

	// An image row without provider usage keeps its caveat when repricing
	// still has no per_image basis.
	update := recalculateEntryCosts(recalculationEntry{
		ID:       "usage-img",
		Model:    "gemini-2.5-flash-image",
		Provider: "gemini",
		Endpoint: "/v1/images/generations",
		RawData:  map[string]any{"images": 1},
		Caveat:   caveatImageMissingUsage,
	}, &recordingPricingResolver{pricing: tokenPricing})
	if update.Caveat != caveatImageMissingUsage {
		t.Fatalf("caveat = %q, want the missing-usage caveat preserved", update.Caveat)
	}

	// Adding a per_image price gives the row a real basis: cost computes and
	// the caveat lifts.
	repriced := recalculateEntryCosts(recalculationEntry{
		ID:       "usage-img",
		Model:    "gemini-2.5-flash-image",
		Provider: "gemini",
		Endpoint: "/v1/images/generations",
		RawData:  map[string]any{"images": 2},
		Caveat:   caveatImageMissingUsage,
	}, &recordingPricingResolver{pricing: &core.ModelPricing{PerImage: new(0.04)}})
	if repriced.Caveat != "" {
		t.Fatalf("caveat = %q, want cleared once per_image prices the row", repriced.Caveat)
	}
	if repriced.TotalCost == nil || *repriced.TotalCost != 0.08 {
		t.Fatalf("total = %v, want 0.08 from the per_image rate", repriced.TotalCost)
	}

	// Embedding rows keep the caveat unconditionally: no repricing can
	// recover usage the provider never reported.
	embedding := recalculateEntryCosts(recalculationEntry{
		ID:       "usage-emb",
		Model:    "gemini-embedding-001",
		Provider: "gemini",
		Endpoint: "/v1/embeddings",
		Caveat:   caveatEmbeddingMissingUsage,
	}, &recordingPricingResolver{pricing: tokenPricing})
	if embedding.Caveat != caveatEmbeddingMissingUsage {
		t.Fatalf("caveat = %q, want the embedding missing-usage caveat preserved", embedding.Caveat)
	}

	// Unrelated caveats are recalculation's own business and are replaced.
	unrelated := recalculateEntryCosts(recalculationEntry{
		ID:       "usage-other",
		Model:    "gpt-4o",
		Provider: "openai",
		Endpoint: "/v1/chat/completions",
		Caveat:   "some stale caveat",
	}, &recordingPricingResolver{pricing: tokenPricing})
	if unrelated.Caveat != "" {
		t.Fatalf("caveat = %q, want unrelated caveats recomputed", unrelated.Caveat)
	}
}
