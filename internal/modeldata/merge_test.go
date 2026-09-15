package modeldata

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergeMetadata_BothNil(t *testing.T) {
	got := MergeMetadata(nil, nil)
	assert.Nil(t, got)
}

func TestMergeMetadata_NilOverride(t *testing.T) {
	base := &core.ModelMetadata{DisplayName: "Base", ContextWindow: new(1024)}
	got := MergeMetadata(base, nil)
	require.NotNil(t, got)
	assert.Equal(t, "Base", got.DisplayName)
	require.NotNil(t, got.ContextWindow)
	assert.Equal(t, 1024, *got.ContextWindow)
	assert.NotSame(t, base, got)
}

func TestMergeMetadata_NilBase(t *testing.T) {
	override := &core.ModelMetadata{DisplayName: "Override"}
	got := MergeMetadata(nil, override)
	require.NotNil(t, got)
	assert.Equal(t, "Override", got.DisplayName)
	assert.NotSame(t, override, got)
}

func TestMergeMetadata_OverrideWinsPerField(t *testing.T) {
	base := &core.ModelMetadata{
		DisplayName:     "Base",
		Description:     "base desc",
		ContextWindow:   new(1024),
		MaxOutputTokens: new(256),
		Modes:           []string{"chat"},
		Capabilities:    map[string]bool{"tools": false, "vision": true},
		Pricing:         &core.ModelPricing{Currency: "USD"},
	}
	override := &core.ModelMetadata{
		DisplayName:   "Overridden",
		ContextWindow: new(131072),
		Capabilities:  map[string]bool{"tools": true},
	}
	got := MergeMetadata(base, override)
	assert.Equal(t, "Overridden", got.DisplayName)
	assert.Equal(t, "base desc", got.Description)
	require.NotNil(t, got.ContextWindow)
	assert.Equal(t, 131072, *got.ContextWindow)
	require.NotNil(t, got.MaxOutputTokens)
	assert.Equal(t, 256, *got.MaxOutputTokens)
	require.Len(t, got.Modes, 1)
	assert.Equal(t, "chat", got.Modes[0])
	assert.True(t, got.Capabilities["tools"])
	assert.True(t, got.Capabilities["vision"])
	require.NotNil(t, got.Pricing)
	assert.Equal(t, "USD", got.Pricing.Currency)
}

func TestMergeMetadata_DoesNotMutateInputs(t *testing.T) {
	base := &core.ModelMetadata{
		DisplayName:  "Base",
		Capabilities: map[string]bool{"tools": false},
	}
	override := &core.ModelMetadata{
		Capabilities: map[string]bool{"tools": true},
	}
	_ = MergeMetadata(base, override)
	if base.Capabilities["tools"] {
		t.Error("base.Capabilities[tools] mutated")
	}
	assert.True(t, override.Capabilities["tools"])
}

func TestMergeMetadata_PassthroughDoesNotAlias(t *testing.T) {
	baseCW := 4096
	base := &core.ModelMetadata{
		DisplayName:   "Base",
		Modes:         []string{"chat"},
		Capabilities:  map[string]bool{"tools": true},
		ContextWindow: &baseCW,
		Pricing:       &core.ModelPricing{Currency: "USD"},
	}

	got := MergeMetadata(base, nil)
	require.NotSame(t, base, got)

	got.Modes[0] = "mutated"
	got.Capabilities["tools"] = false
	*got.ContextWindow = 0
	got.Pricing.Currency = "EUR"

	assert.Equal(t, "chat", base.Modes[0], "base.Modes mutated through clone: %v", base.Modes)
	assert.True(t, base.Capabilities["tools"])
	require.NotNil(t, base.ContextWindow)
	assert.Equal(t, 4096, *base.ContextWindow)
	assert.Equal(t, "USD", base.Pricing.Currency)
}

func TestMergeMetadata_MergedResultDoesNotAliasBase(t *testing.T) {
	baseCW := 4096
	base := &core.ModelMetadata{
		Modes:         []string{"chat"},
		Capabilities:  map[string]bool{"tools": true},
		ContextWindow: &baseCW,
		Pricing:       &core.ModelPricing{Currency: "USD"},
	}
	// Override only a field the base doesn't touch so merged's Modes/Pricing/etc
	// come from base and must still be independent.
	override := &core.ModelMetadata{DisplayName: "Overridden"}

	got := MergeMetadata(base, override)

	got.Modes[0] = "mutated"
	got.Capabilities["tools"] = false
	*got.ContextWindow = 0
	got.Pricing.Currency = "EUR"

	assert.Equal(t, "chat", base.Modes[0], "base.Modes aliased: %v", base.Modes)
	assert.True(t, base.Capabilities["tools"])
	require.NotNil(t, base.ContextWindow)
	assert.Equal(t, 4096, *base.ContextWindow)
	assert.Equal(t, "USD", base.Pricing.Currency)
}

func TestMergeMetadata_OverrideRankingsDoNotAlias(t *testing.T) {
	baseElo := 1500.0
	baseRank := 3
	base := &core.ModelMetadata{
		Rankings: map[string]core.ModelRanking{
			"base-only": {Elo: &baseElo, Rank: &baseRank, AsOf: "2025-01-01"},
		},
	}
	overElo := 2000.0
	overRank := 1
	override := &core.ModelMetadata{
		Rankings: map[string]core.ModelRanking{
			"overridden": {Elo: &overElo, Rank: &overRank, AsOf: "2025-06-01"},
		},
	}

	got := MergeMetadata(base, override)

	// Mutate through the merged result.
	*got.Rankings["overridden"].Elo = 0
	*got.Rankings["overridden"].Rank = 0
	*got.Rankings["base-only"].Elo = 0
	*got.Rankings["base-only"].Rank = 0

	require.NotNil(t, override.Rankings["overridden"].Elo)
	assert.Equal(t, 2000.0, *override.Rankings["overridden"].Elo)
	require.NotNil(t, override.Rankings["overridden"].Rank)
	assert.Equal(t, 1, *override.Rankings["overridden"].Rank)
	require.NotNil(t, base.Rankings["base-only"].Elo)
	assert.Equal(t, 1500.0, *base.Rankings["base-only"].Elo)
	require.NotNil(t, base.Rankings["base-only"].Rank)
	assert.Equal(t, 3, *base.Rankings["base-only"].Rank)
}

func TestMergeMetadata_OverridePricingReplaces(t *testing.T) {
	basePrice := 1.0
	base := &core.ModelMetadata{
		Pricing: &core.ModelPricing{Currency: "USD", InputPerMtok: &basePrice},
	}
	overPrice := 0.0
	override := &core.ModelMetadata{
		Pricing: &core.ModelPricing{Currency: "USD", InputPerMtok: &overPrice},
	}
	got := MergeMetadata(base, override)
	require.NotNil(t, got.Pricing)
	require.NotNil(t, got.Pricing.InputPerMtok)
	assert.Equal(t, 0.0, *got.Pricing.InputPerMtok)

	// Ensure we didn't mutate the override's pricing pointer into base's or vice versa.
	assert.NotSame(t, override.Pricing, got.Pricing)
}
