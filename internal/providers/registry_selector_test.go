package providers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// TestRegistrySelectorLookupMatchesStringLookup pins the selector-keyed fast
// path to the qualified-string lookup it shortcuts: for every selector shape
// (provider bucket hit, raw slash-shaped model ID, unknown model, unknown
// provider, configured provider with a foreign model, empty parts), both
// paths must return the same entry.
func TestRegistrySelectorLookupMatchesStringLookup(t *testing.T) {
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
	require.NoError(t, registry.Initialize(context.Background()))

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
			assert.Same(t, registry.GetModel(qualified), registry.GetModelForSelector(selector))
		})
	}
}
