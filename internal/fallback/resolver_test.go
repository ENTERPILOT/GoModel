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
	return &providers.ModelInfo{
		Model: core.Model{
			ID: id,
			Metadata: &core.ModelMetadata{
				Family:     family,
				Categories: []core.ModelCategory{core.CategoryTextGeneration},
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
