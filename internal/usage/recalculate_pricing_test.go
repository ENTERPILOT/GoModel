package usage

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
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

	require.Equal(t, "gpt-4o", resolver.model)
	require.Equal(t, "primary-openai", resolver.provider)
	require.NotNil(t, update.InputCost)
	require.Equal(t, 1.25, *update.InputCost)
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
	require.Equal(t, caveatImageMissingUsage, update.Caveat)

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
	require.Empty(t, repriced.Caveat)
	require.NotNil(t, repriced.TotalCost)
	require.Equal(t, 0.08, *repriced.TotalCost)

	// A per_image price cannot lift the caveat for a row that recorded no
	// image count — there is nothing to price.
	countless := recalculateEntryCosts(recalculationEntry{
		ID:       "usage-img-empty",
		Model:    "gemini-2.5-flash-image",
		Provider: "gemini",
		Endpoint: "/v1/images/generations",
		Caveat:   caveatImageMissingUsage,
	}, &recordingPricingResolver{pricing: &core.ModelPricing{PerImage: new(0.04)}})
	require.Equal(t, caveatImageMissingUsage, countless.Caveat)

	// A caveat compounded by an earlier recalculation still matches; only the
	// canonical missing-usage part survives re-joining.
	compound := recalculateEntryCosts(recalculationEntry{
		ID:       "usage-img-compound",
		Model:    "gemini-2.5-flash-image",
		Provider: "gemini",
		Endpoint: "/v1/images/generations",
		RawData:  map[string]any{"images": 1},
		Caveat:   caveatImageMissingUsage + "; unmapped token field: foo",
	}, &recordingPricingResolver{pricing: tokenPricing})
	require.Equal(t, caveatImageMissingUsage, compound.Caveat)

	// Embedding rows keep the caveat unconditionally: no repricing can
	// recover usage the provider never reported.
	embedding := recalculateEntryCosts(recalculationEntry{
		ID:       "usage-emb",
		Model:    "gemini-embedding-001",
		Provider: "gemini",
		Endpoint: "/v1/embeddings",
		Caveat:   caveatEmbeddingMissingUsage,
	}, &recordingPricingResolver{pricing: tokenPricing})
	require.Equal(t, caveatEmbeddingMissingUsage, embedding.Caveat)

	// Repricing an embedding row onto usage-independent pricing lifts the
	// caveat: the recalculated cost no longer depends on the missing usage.
	embeddingRepriced := recalculateEntryCosts(recalculationEntry{
		ID:       "usage-emb-flat",
		Model:    "gemini-embedding-001",
		Provider: "gemini",
		Endpoint: "/v1/embeddings",
		Caveat:   caveatEmbeddingMissingUsage,
	}, &recordingPricingResolver{pricing: &core.ModelPricing{PerRequest: new(0.01)}})
	require.Empty(t, embeddingRepriced.Caveat)

	// Unrelated caveats are recalculation's own business and are replaced.
	unrelated := recalculateEntryCosts(recalculationEntry{
		ID:       "usage-other",
		Model:    "gpt-4o",
		Provider: "openai",
		Endpoint: "/v1/chat/completions",
		Caveat:   "some stale caveat",
	}, &recordingPricingResolver{pricing: tokenPricing})
	require.Empty(t, unrelated.Caveat)
}
