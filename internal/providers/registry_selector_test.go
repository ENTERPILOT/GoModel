package providers

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

// TestRegistrySelectorLookupsMatchStringLookups pins the selector-keyed fast
// path to the qualified-string lookups it shortcuts: for every selector shape
// (provider bucket hit, raw slash-shaped model ID, unknown model, unknown
// provider, configured provider with a foreign model, empty parts), both
// paths must answer identically.
func TestRegistrySelectorLookupsMatchStringLookups(t *testing.T) {
	registry := NewModelRegistry()
	registry.RegisterProviderWithNameAndType(&registryMockProvider{
		name: "openai-primary",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data:   []core.Model{{ID: "gpt-4o", Object: "model", OwnedBy: "openai"}},
		},
	}, "openai-primary", "openai")
	registry.RegisterProviderWithNameAndType(&registryMockProvider{
		name: "groq",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data:   []core.Model{{ID: "meta-llama/llama-3-70b", Object: "model", OwnedBy: "meta"}},
		},
	}, "groq", "groq")
	if err := registry.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	selectors := []core.ModelSelector{
		{Provider: "openai-primary", Model: "gpt-4o"},
		{Provider: "groq", Model: "meta-llama/llama-3-70b"},
		{Provider: "meta-llama", Model: "llama-3-70b"},
		{Provider: "", Model: "gpt-4o"},
		{Provider: "", Model: "meta-llama/llama-3-70b"},
		{Provider: "openai-primary", Model: "missing-model"},
		{Provider: "unknown-provider", Model: "gpt-4o"},
		{Provider: "", Model: "missing"},
		{Provider: "openai-primary", Model: ""},
		{Provider: "", Model: ""},
	}

	for _, selector := range selectors {
		qualified := selector.QualifiedModel()
		t.Run(qualified, func(t *testing.T) {
			if got, want := registry.SupportsSelector(selector), registry.Supports(qualified); got != want {
				t.Errorf("SupportsSelector = %v, Supports(%q) = %v", got, qualified, want)
			}
			if got, want := registry.GetProviderForSelector(selector), registry.GetProvider(qualified); got != want {
				t.Errorf("GetProviderForSelector = %v, GetProvider(%q) = %v", got, qualified, want)
			}
			if got, want := registry.GetProviderTypeForSelector(selector), registry.GetProviderType(qualified); got != want {
				t.Errorf("GetProviderTypeForSelector = %q, GetProviderType(%q) = %q", got, qualified, want)
			}
			if got, want := registry.GetProviderNameForSelector(selector), registry.GetProviderName(qualified); got != want {
				t.Errorf("GetProviderNameForSelector = %q, GetProviderName(%q) = %q", got, qualified, want)
			}
		})
	}
}
