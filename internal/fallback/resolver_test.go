package fallback

import (
	"testing"

	"gomodel/config"
	"gomodel/internal/core"
	"gomodel/internal/providers"
)

type fakeRegistry struct {
	byKey  map[string]*providers.ModelInfo
	models []providers.ModelWithProvider
}

func (r *fakeRegistry) GetModel(model string) *providers.ModelInfo {
	return r.byKey[model]
}

func (r *fakeRegistry) ListModelsWithProvider() []providers.ModelWithProvider {
	return append([]providers.ModelWithProvider(nil), r.models...)
}

func TestResolverManualModeUsesConfiguredFallbacks(t *testing.T) {
	registry := newFakeRegistry(
		modelInfo("gpt-4o", "openai", "openai", 1287, "gpt-4o"),
		modelInfo("gpt-4o", "azure", "azure", 1287, "gpt-4o"),
		modelInfo("gemini-2.5-pro", "gemini", "gemini", 1290, "gemini-2.5-pro"),
	)

	resolver := NewResolver(config.FallbackConfig{
		DefaultMode: config.FallbackModeOff,
		Manual: map[string][]string{
			"gpt-4o": []string{"azure/gpt-4o", "gemini/gemini-2.5-pro"},
		},
		Overrides: map[string]config.FallbackModelOverride{
			"gpt-4o": {Mode: config.FallbackModeManual},
		},
	}, registry)

	got := resolver.ResolveFallbacks(&core.RequestModelResolution{
		Requested:        core.NewRequestedModelSelector("gpt-4o", ""),
		ResolvedSelector: core.ModelSelector{Model: "gpt-4o"},
		ProviderType:     "openai",
	}, core.OperationChatCompletions)

	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0].QualifiedModel() != "azure/gpt-4o" {
		t.Fatalf("got[0] = %q, want %q", got[0].QualifiedModel(), "azure/gpt-4o")
	}
	if got[1].QualifiedModel() != "gemini/gemini-2.5-pro" {
		t.Fatalf("got[1] = %q, want %q", got[1].QualifiedModel(), "gemini/gemini-2.5-pro")
	}
}

func TestResolverAutoModeAppendsRankingCandidates(t *testing.T) {
	registry := newFakeRegistry(
		modelInfo("gpt-4o", "openai", "openai", 1287, "gpt-4o"),
		modelInfo("gpt-4o", "azure", "azure", 1287, "gpt-4o"),
		modelInfo("gemini-2.5-pro", "gemini", "gemini", 1290, "gemini-2.5-pro"),
		modelInfo("claude-sonnet-4", "anthropic", "anthropic", 1305, "claude-sonnet"),
	)

	resolver := NewResolver(config.FallbackConfig{
		DefaultMode: config.FallbackModeAuto,
		Manual: map[string][]string{
			"gpt-4o": []string{"azure/gpt-4o"},
		},
	}, registry)

	got := resolver.ResolveFallbacks(&core.RequestModelResolution{
		Requested:        core.NewRequestedModelSelector("gpt-4o", ""),
		ResolvedSelector: core.ModelSelector{Model: "gpt-4o"},
		ProviderType:     "openai",
	}, core.OperationChatCompletions)

	if len(got) < 3 {
		t.Fatalf("len(got) = %d, want at least 3", len(got))
	}
	if got[0].QualifiedModel() != "azure/gpt-4o" {
		t.Fatalf("got[0] = %q, want %q", got[0].QualifiedModel(), "azure/gpt-4o")
	}
	if got[1].QualifiedModel() != "gemini/gemini-2.5-pro" {
		t.Fatalf("got[1] = %q, want %q", got[1].QualifiedModel(), "gemini/gemini-2.5-pro")
	}
	if got[2].QualifiedModel() != "anthropic/claude-sonnet-4" {
		t.Fatalf("got[2] = %q, want %q", got[2].QualifiedModel(), "anthropic/claude-sonnet-4")
	}
}

func TestResolverBlankDefaultModeUsesManualFallback(t *testing.T) {
	registry := newFakeRegistry(
		modelInfo("gpt-4o", "openai", "openai", 1287, "gpt-4o"),
		modelInfo("gpt-4o", "azure", "azure", 1287, "gpt-4o"),
		modelInfo("gemini-2.5-pro", "gemini", "gemini", 1290, "gemini-2.5-pro"),
	)

	resolver := NewResolver(config.FallbackConfig{}, registry)
	if resolver == nil {
		t.Fatal("NewResolver() = nil, want manual-enabled resolver")
	}

	got := resolver.ResolveFallbacks(&core.RequestModelResolution{
		Requested:        core.NewRequestedModelSelector("gpt-4o", ""),
		ResolvedSelector: core.ModelSelector{Model: "gpt-4o"},
		ProviderType:     "openai",
	}, core.OperationChatCompletions)

	if len(got) != 0 {
		t.Fatalf("len(got) = %d, want 0 without manual rules", len(got))
	}
}

func TestResolverOverrideOffDisablesFallbacks(t *testing.T) {
	registry := newFakeRegistry(
		modelInfo("gpt-4o", "openai", "openai", 1287, "gpt-4o"),
		modelInfo("gpt-4o", "azure", "azure", 1287, "gpt-4o"),
	)

	resolver := NewResolver(config.FallbackConfig{
		DefaultMode: config.FallbackModeAuto,
		Manual: map[string][]string{
			"gpt-4o": []string{"azure/gpt-4o"},
		},
		Overrides: map[string]config.FallbackModelOverride{
			"gpt-4o": {Mode: config.FallbackModeOff},
		},
	}, registry)

	got := resolver.ResolveFallbacks(&core.RequestModelResolution{
		Requested:        core.NewRequestedModelSelector("gpt-4o", ""),
		ResolvedSelector: core.ModelSelector{Model: "gpt-4o"},
		ProviderType:     "openai",
	}, core.OperationChatCompletions)

	if len(got) != 0 {
		t.Fatalf("len(got) = %d, want 0", len(got))
	}
}

func TestResolverDoesNotReturnFallbacksForEmbeddings(t *testing.T) {
	registry := newFakeRegistry(
		modelInfoWithCategories("text-embedding-3-small", "openai", "openai", 1287, "text-embedding-3", core.CategoryEmbedding),
		modelInfoWithCategories("text-embedding-3-large", "azure", "azure", 1288, "text-embedding-3", core.CategoryEmbedding),
	)

	resolver := NewResolver(config.FallbackConfig{
		DefaultMode: config.FallbackModeAuto,
		Manual: map[string][]string{
			"text-embedding-3-small": []string{"azure/text-embedding-3-large"},
		},
	}, registry)

	got := resolver.ResolveFallbacks(&core.RequestModelResolution{
		Requested:        core.NewRequestedModelSelector("text-embedding-3-small", ""),
		ResolvedSelector: core.ModelSelector{Model: "text-embedding-3-small", Provider: "openai"},
		ProviderType:     "openai",
	}, core.OperationEmbeddings)

	if len(got) != 0 {
		t.Fatalf("len(got) = %d, want 0", len(got))
	}
}

func TestResolverPrefersProviderQualifiedOverrideForBareRequests(t *testing.T) {
	registry := newFakeRegistry(
		modelInfo("gpt-4o", "openai", "openai", 1287, "gpt-4o"),
		modelInfo("gpt-4o", "azure", "azure", 1287, "gpt-4o"),
		modelInfo("gemini-2.5-pro", "gemini", "gemini", 1290, "gemini-2.5-pro"),
	)

	resolver := NewResolver(config.FallbackConfig{
		DefaultMode: config.FallbackModeAuto,
		Manual: map[string][]string{
			"gpt-4o":        []string{"gemini/gemini-2.5-pro"},
			"openai/gpt-4o": []string{"azure/gpt-4o"},
		},
		Overrides: map[string]config.FallbackModelOverride{
			"gpt-4o":        {Mode: config.FallbackModeOff},
			"openai/gpt-4o": {Mode: config.FallbackModeManual},
		},
	}, registry)

	got := resolver.ResolveFallbacks(&core.RequestModelResolution{
		Requested:        core.NewRequestedModelSelector("gpt-4o", ""),
		ResolvedSelector: core.ModelSelector{Model: "gpt-4o", Provider: "openai"},
		ProviderType:     "openai",
	}, core.OperationChatCompletions)

	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].QualifiedModel() != "azure/gpt-4o" {
		t.Fatalf("got[0] = %q, want %q", got[0].QualifiedModel(), "azure/gpt-4o")
	}
}

func TestResolverTreatsBareModelIDsContainingSlashAsGenericKeys(t *testing.T) {
	registry := newFakeRegistry(
		modelInfo("meta-llama/Meta-Llama-3-70B", "openrouter", "openrouter", 1287, "llama-3"),
		modelInfo("meta-llama/Meta-Llama-3-70B", "groq", "groq", 1287, "llama-3"),
	)

	resolver := NewResolver(config.FallbackConfig{
		DefaultMode: config.FallbackModeAuto,
		Manual: map[string][]string{
			"openrouter/meta-llama/Meta-Llama-3-70B": {"groq/meta-llama/Meta-Llama-3-70B"},
		},
		Overrides: map[string]config.FallbackModelOverride{
			"meta-llama/Meta-Llama-3-70B":            {Mode: config.FallbackModeOff},
			"openrouter/meta-llama/Meta-Llama-3-70B": {Mode: config.FallbackModeManual},
		},
	}, registry)

	got := resolver.ResolveFallbacks(&core.RequestModelResolution{
		Requested:        core.NewRequestedModelSelector("meta-llama/Meta-Llama-3-70B", ""),
		ResolvedSelector: core.ModelSelector{Model: "meta-llama/Meta-Llama-3-70B", Provider: "openrouter"},
		ProviderType:     "openrouter",
	}, core.OperationChatCompletions)

	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].QualifiedModel() != "groq/meta-llama/Meta-Llama-3-70B" {
		t.Fatalf("got[0] = %q, want %q", got[0].QualifiedModel(), "groq/meta-llama/Meta-Llama-3-70B")
	}
}

func TestSameFamily_IgnoresSurroundingWhitespace(t *testing.T) {
	source := &core.ModelMetadata{Family: " gpt-4o "}
	candidate := &core.ModelMetadata{Family: "gpt-4o"}

	if !sameFamily(source, candidate) {
		t.Fatal("expected sameFamily to compare trimmed family values")
	}
}

func newFakeRegistry(infos ...*providers.ModelInfo) *fakeRegistry {
	registry := &fakeRegistry{
		byKey:  make(map[string]*providers.ModelInfo),
		models: make([]providers.ModelWithProvider, 0, len(infos)),
	}

	for _, info := range infos {
		if _, exists := registry.byKey[info.Model.ID]; !exists {
			registry.byKey[info.Model.ID] = info
		}
		registry.byKey[info.ProviderName+"/"+info.Model.ID] = info
		registry.models = append(registry.models, providers.ModelWithProvider{
			Model:        info.Model,
			ProviderType: info.ProviderType,
			ProviderName: info.ProviderName,
			Selector:     info.ProviderName + "/" + info.Model.ID,
		})
	}

	return registry
}

func modelInfo(id, providerName, providerType string, elo float64, family string) *providers.ModelInfo {
	return modelInfoWithCategories(id, providerName, providerType, elo, family, core.CategoryTextGeneration)
}

func modelInfoWithCategories(
	id, providerName, providerType string,
	elo float64,
	family string,
	categories ...core.ModelCategory,
) *providers.ModelInfo {
	return &providers.ModelInfo{
		Model: core.Model{
			ID: id,
			Metadata: &core.ModelMetadata{
				Family:     family,
				Categories: append([]core.ModelCategory(nil), categories...),
				Capabilities: map[string]bool{
					"streaming": true,
				},
				Rankings: map[string]core.ModelRanking{
					"chatbot_arena": {
						Elo:  &elo,
						Rank: intPtr(1),
						AsOf: "2026-02-22",
					},
				},
			},
		},
		ProviderName: providerName,
		ProviderType: providerType,
	}
}

func intPtr(v int) *int {
	return &v
}
