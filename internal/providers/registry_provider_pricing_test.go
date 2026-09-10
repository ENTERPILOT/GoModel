package providers

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/modeldata"
)

// edenPricedModel mirrors what a dynamic-metadata provider (Eden AI, Chutes,
// OpenRouter) returns from ListModels: the model plus the pricing and context
// window it read off its own catalog.
func edenPricedModel() core.Model {
	return core.Model{
		ID:      "openai/gpt-4",
		Object:  "model",
		OwnedBy: "openai",
		Metadata: &core.ModelMetadata{
			Modes:         []string{"chat", "responses"},
			Categories:    core.CategoriesForModes([]string{"chat", "responses"}),
			Capabilities:  map[string]bool{"reasoning": true},
			ContextWindow: new(131072),
			Pricing: &core.ModelPricing{
				Currency:           "USD",
				InputPerMtok:       new(0.06),
				OutputPerMtok:      new(0.18),
				CachedInputPerMtok: new(0.012),
			},
		},
	}
}

// TestInitialize_ProviderReportedPricingSurvivesCatalogMiss is the registry
// half of dynamic provider metadata. A gateway-of-gateways like Eden AI has
// none of its models in the central catalog (entries are keyed
// "<providerType>/<modelID>", so "edenai/openai/gpt-4" never resolves), and
// enrichment must therefore fall back to what the provider itself reported
// rather than dropping it. Without that, pricing never reaches
// ResolvePricing, and Eden spend stays invisible to budgets and cost routing.
func TestInitialize_ProviderReportedPricingSurvivesCatalogMiss(t *testing.T) {
	registry := NewModelRegistry()
	provider := &registryMockProvider{
		name: "provider-edenai",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data:   []core.Model{edenPricedModel()},
		},
	}
	registry.RegisterProviderWithNameAndType(provider, "edenai", "edenai")

	if err := registry.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// A populated catalog that knows nothing about this provider: the exact
	// condition Eden models are always in.
	registry.setModelListAndEnrich(&modeldata.ModelList{
		Models: map[string]modeldata.ModelEntry{},
		ProviderModels: map[string]modeldata.ProviderModelEntry{
			"openai/gpt-4": {},
		},
	}, nil, "etag", "https://example.invalid/models.json")

	info := registry.GetModel("edenai/openai/gpt-4")
	if info == nil {
		t.Fatal("model not registered under its provider-qualified ID")
	}
	if info.Discovered == nil || info.Discovered.Pricing == nil {
		t.Fatalf("Discovered = %+v, want the provider's own report retained", info.Discovered)
	}
	meta := info.Model.Metadata
	if meta == nil || meta.Pricing == nil {
		t.Fatalf("Metadata = %+v, want provider pricing to survive enrichment", meta)
	}
	assertPricePtr(t, "InputPerMtok", meta.Pricing.InputPerMtok, 0.06)
	assertPricePtr(t, "OutputPerMtok", meta.Pricing.OutputPerMtok, 0.18)
	assertPricePtr(t, "CachedInputPerMtok", meta.Pricing.CachedInputPerMtok, 0.012)
	if meta.ContextWindow == nil || *meta.ContextWindow != 131072 {
		t.Errorf("ContextWindow = %v, want 131072", meta.ContextWindow)
	}
	if !meta.Capabilities["reasoning"] {
		t.Errorf("Capabilities = %v, want reasoning retained", meta.Capabilities)
	}
}

// TestResolvePricing_UsesProviderReportedPricing closes the loop to the cost
// path: ResolvePricing is what internal/usage calls per request, and it must
// find the dynamically discovered rates.
func TestResolvePricing_UsesProviderReportedPricing(t *testing.T) {
	registry := NewModelRegistry()
	provider := &registryMockProvider{
		name: "provider-edenai",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data:   []core.Model{edenPricedModel()},
		},
	}
	registry.RegisterProviderWithNameAndType(provider, "edenai", "edenai")

	if err := registry.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	for _, selector := range []string{"edenai/openai/gpt-4", "openai/gpt-4"} {
		pricing := registry.ResolvePricing(selector, "edenai")
		if pricing == nil {
			t.Fatalf("ResolvePricing(%q) = nil, want the provider-reported rates", selector)
		}
		assertPricePtr(t, selector+" InputPerMtok", pricing.InputPerMtok, 0.06)
		assertPricePtr(t, selector+" OutputPerMtok", pricing.OutputPerMtok, 0.18)
	}
}

// TestModelFilter_AdmitsProviderPricedModels pins the pre-request consumer of
// dynamic pricing: a max-price filter drops unpriced models by design, so a
// provider that reports no pricing has its whole catalog excluded. Reporting
// pricing is what makes the cap usable.
func TestModelFilter_AdmitsProviderPricedModels(t *testing.T) {
	priced := edenPricedModel()
	unpriced := core.Model{ID: "openai/gpt-5", Object: "model"}

	filter, active := newModelFilter(config.ModelFilter{MaxPricePerMtok: new(1.0)})
	if !active {
		t.Fatal("newModelFilter reported an inactive filter for a price cap")
	}
	if !filter.keep(priced) {
		t.Error("priced model rejected by a 1.0/MTok cap, want admitted (0.18 max rate)")
	}
	if filter.keep(unpriced) {
		t.Error("unpriced model admitted by a price cap, want dropped")
	}
}

func assertPricePtr(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Errorf("%s = nil, want %v", name, want)
		return
	}
	if *got != want {
		t.Errorf("%s = %v, want %v", name, *got, want)
	}
}
