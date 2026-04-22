package modeldata

import (
	"testing"

	"gomodel/internal/core"
)

func intPtr(v int) *int { return &v }

func TestMergeMetadata_BothNil(t *testing.T) {
	if got := MergeMetadata(nil, nil); got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}

func TestMergeMetadata_NilOverride(t *testing.T) {
	base := &core.ModelMetadata{DisplayName: "Base", ContextWindow: intPtr(1024)}
	got := MergeMetadata(base, nil)
	if got == nil || got.DisplayName != "Base" || *got.ContextWindow != 1024 {
		t.Errorf("got %+v", got)
	}
	if got == base {
		t.Error("expected a clone, got same pointer")
	}
}

func TestMergeMetadata_NilBase(t *testing.T) {
	override := &core.ModelMetadata{DisplayName: "Override"}
	got := MergeMetadata(nil, override)
	if got == nil || got.DisplayName != "Override" {
		t.Errorf("got %+v", got)
	}
	if got == override {
		t.Error("expected a clone, got same pointer")
	}
}

func TestMergeMetadata_OverrideWinsPerField(t *testing.T) {
	base := &core.ModelMetadata{
		DisplayName:     "Base",
		Description:     "base desc",
		ContextWindow:   intPtr(1024),
		MaxOutputTokens: intPtr(256),
		Modes:           []string{"chat"},
		Capabilities:    map[string]bool{"tools": false, "vision": true},
		Pricing:         &core.ModelPricing{Currency: "USD"},
	}
	override := &core.ModelMetadata{
		DisplayName:   "Overridden",
		ContextWindow: intPtr(131072),
		Capabilities:  map[string]bool{"tools": true},
	}
	got := MergeMetadata(base, override)
	if got.DisplayName != "Overridden" {
		t.Errorf("DisplayName = %q, want Overridden", got.DisplayName)
	}
	if got.Description != "base desc" {
		t.Errorf("Description = %q, want base desc (preserved)", got.Description)
	}
	if got.ContextWindow == nil || *got.ContextWindow != 131072 {
		t.Errorf("ContextWindow = %v, want 131072", got.ContextWindow)
	}
	if got.MaxOutputTokens == nil || *got.MaxOutputTokens != 256 {
		t.Errorf("MaxOutputTokens = %v, want 256 (preserved)", got.MaxOutputTokens)
	}
	if len(got.Modes) != 1 || got.Modes[0] != "chat" {
		t.Errorf("Modes = %v, want [chat] (preserved)", got.Modes)
	}
	if !got.Capabilities["tools"] {
		t.Errorf("Capabilities[tools] = false, want true (override)")
	}
	if !got.Capabilities["vision"] {
		t.Errorf("Capabilities[vision] = false, want true (preserved)")
	}
	if got.Pricing == nil || got.Pricing.Currency != "USD" {
		t.Errorf("Pricing = %+v, want USD (preserved)", got.Pricing)
	}
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
	if !override.Capabilities["tools"] {
		t.Error("override.Capabilities[tools] mutated")
	}
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
	if got == base {
		t.Fatal("expected a clone, got same pointer")
	}

	got.Modes[0] = "mutated"
	got.Capabilities["tools"] = false
	*got.ContextWindow = 0
	got.Pricing.Currency = "EUR"

	if base.Modes[0] != "chat" {
		t.Errorf("base.Modes mutated through clone: %v", base.Modes)
	}
	if !base.Capabilities["tools"] {
		t.Error("base.Capabilities mutated through clone")
	}
	if *base.ContextWindow != 4096 {
		t.Errorf("base.ContextWindow mutated through clone: %d", *base.ContextWindow)
	}
	if base.Pricing.Currency != "USD" {
		t.Errorf("base.Pricing mutated through clone: %q", base.Pricing.Currency)
	}
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

	if base.Modes[0] != "chat" {
		t.Errorf("base.Modes aliased: %v", base.Modes)
	}
	if !base.Capabilities["tools"] {
		t.Error("base.Capabilities aliased")
	}
	if *base.ContextWindow != 4096 {
		t.Errorf("base.ContextWindow aliased: %d", *base.ContextWindow)
	}
	if base.Pricing.Currency != "USD" {
		t.Errorf("base.Pricing aliased: %q", base.Pricing.Currency)
	}
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
	if got.Pricing == nil || got.Pricing.InputPerMtok == nil || *got.Pricing.InputPerMtok != 0.0 {
		t.Errorf("Pricing = %+v", got.Pricing)
	}
	// Ensure we didn't mutate the override's pricing pointer into base's or vice versa.
	if got.Pricing == override.Pricing {
		t.Error("expected a clone of override.Pricing, got same pointer")
	}
}
