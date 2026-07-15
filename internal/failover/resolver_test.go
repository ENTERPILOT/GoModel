package failover

import (
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
)

// NewResolver builds a failover resolver from config and the current model
// inventory without a dynamic manual-rule provider. Test-only convenience
// over NewResolverWithRuleProvider.
func NewResolver(cfg config.FailoverConfig, registry Registry) *Resolver {
	return NewResolverWithRuleProvider(cfg, registry, nil)
}

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

func TestResolverManualModeUsesConfiguredFailovers(t *testing.T) {
	registry := newFakeRegistry(
		modelInfo("gpt-4o", "openai", "openai", 1287, "gpt-4o"),
		modelInfo("gpt-4o", "azure", "azure", 1287, "gpt-4o"),
		modelInfo("gemini-2.5-pro", "gemini", "gemini", 1290, "gemini-2.5-pro"),
	)

	resolver := NewResolver(config.FailoverConfig{
		Enabled: true,
		Manual: map[string][]string{
			"gpt-4o": []string{"azure/gpt-4o", "gemini/gemini-2.5-pro"},
		},
	}, registry)

	got := resolver.ResolveFailovers(&core.RequestModelResolution{
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

func TestResolverSuggestFailoversReturnsRankingCandidates(t *testing.T) {
	registry := newFakeRegistry(
		modelInfo("gpt-4o", "openai", "openai", 1287, "gpt-4o"),
		modelInfo("gpt-4o", "azure", "azure", 1287, "gpt-4o"),
		modelInfo("gemini-2.5-pro", "gemini", "gemini", 1290, "gemini-2.5-pro"),
		modelInfo("claude-sonnet-4", "anthropic", "anthropic", 1305, "claude-sonnet"),
	)

	resolver := NewResolver(config.FailoverConfig{
		Enabled: true,
		Manual: map[string][]string{
			"gpt-4o": []string{"azure/gpt-4o"},
		},
	}, registry)

	got := resolver.SuggestFailovers(&core.RequestModelResolution{
		Requested:        core.NewRequestedModelSelector("gpt-4o", ""),
		ResolvedSelector: core.ModelSelector{Model: "gpt-4o"},
		ProviderType:     "openai",
	}, core.OperationChatCompletions)

	if len(got) < 2 {
		t.Fatalf("len(got) = %d, want at least 2", len(got))
	}
	if got[0].QualifiedModel() != "gemini/gemini-2.5-pro" {
		t.Fatalf("got[0] = %q, want %q", got[0].QualifiedModel(), "gemini/gemini-2.5-pro")
	}
	if got[1].QualifiedModel() != "anthropic/claude-sonnet-4" {
		t.Fatalf("got[1] = %q, want %q", got[1].QualifiedModel(), "anthropic/claude-sonnet-4")
	}
}

func TestResolverBlankDefaultModeUsesManualFailover(t *testing.T) {
	registry := newFakeRegistry(
		modelInfo("gpt-4o", "openai", "openai", 1287, "gpt-4o"),
		modelInfo("gpt-4o", "azure", "azure", 1287, "gpt-4o"),
		modelInfo("gemini-2.5-pro", "gemini", "gemini", 1290, "gemini-2.5-pro"),
	)

	resolver := NewResolver(config.FailoverConfig{Enabled: true}, registry)
	if resolver == nil {
		t.Fatal("NewResolver() = nil, want manual-enabled resolver")
	}

	got := resolver.ResolveFailovers(&core.RequestModelResolution{
		Requested:        core.NewRequestedModelSelector("gpt-4o", ""),
		ResolvedSelector: core.ModelSelector{Model: "gpt-4o"},
		ProviderType:     "openai",
	}, core.OperationChatCompletions)

	if len(got) != 0 {
		t.Fatalf("len(got) = %d, want 0 without manual rules", len(got))
	}
}

func TestResolverOverrideOffDisablesFailovers(t *testing.T) {
	registry := newFakeRegistry(
		modelInfo("gpt-4o", "openai", "openai", 1287, "gpt-4o"),
		modelInfo("gpt-4o", "azure", "azure", 1287, "gpt-4o"),
	)

	resolver := NewResolver(config.FailoverConfig{
		Enabled: true,
		Manual: map[string][]string{
			"gpt-4o": []string{"azure/gpt-4o"},
		},
		Disabled: map[string]bool{
			"gpt-4o": true,
		},
	}, registry)

	got := resolver.ResolveFailovers(&core.RequestModelResolution{
		Requested:        core.NewRequestedModelSelector("gpt-4o", ""),
		ResolvedSelector: core.ModelSelector{Model: "gpt-4o"},
		ProviderType:     "openai",
	}, core.OperationChatCompletions)

	if len(got) != 0 {
		t.Fatalf("len(got) = %d, want 0", len(got))
	}
}

func TestResolverDoesNotReturnFailoversForEmbeddings(t *testing.T) {
	registry := newFakeRegistry(
		modelInfoWithCategories("text-embedding-3-small", "openai", "openai", 1287, "text-embedding-3", core.CategoryEmbedding),
		modelInfoWithCategories("text-embedding-3-large", "azure", "azure", 1288, "text-embedding-3", core.CategoryEmbedding),
	)

	resolver := NewResolver(config.FailoverConfig{
		Enabled: true,
		Manual: map[string][]string{
			"text-embedding-3-small": []string{"azure/text-embedding-3-large"},
		},
	}, registry)

	got := resolver.ResolveFailovers(&core.RequestModelResolution{
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

	resolver := NewResolver(config.FailoverConfig{
		Enabled: true,
		Manual: map[string][]string{
			"gpt-4o":        []string{"gemini/gemini-2.5-pro"},
			"openai/gpt-4o": []string{"azure/gpt-4o"},
		},
	}, registry)

	got := resolver.ResolveFailovers(&core.RequestModelResolution{
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

	resolver := NewResolver(config.FailoverConfig{
		Enabled: true,
		Manual: map[string][]string{
			"openrouter/meta-llama/Meta-Llama-3-70B": {"groq/meta-llama/Meta-Llama-3-70B"},
		},
	}, registry)

	got := resolver.ResolveFailovers(&core.RequestModelResolution{
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

type fakeRuleProvider struct {
	rules    map[string][]string
	disabled map[string]bool
}

func (p *fakeRuleProvider) Rules() map[string][]string { return p.rules }
func (p *fakeRuleProvider) Disabled() map[string]bool  { return p.disabled }

// Dynamic rules from a RuleProvider win per-key over static config rules, which
// is the runtime path the failover service uses (admin edits override YAML).
func TestResolverDynamicRuleProviderOverridesStaticRules(t *testing.T) {
	registry := newFakeRegistry(
		modelInfo("gpt-4o", "openai", "openai", 1287, "gpt-4o"),
		modelInfo("gpt-4o", "azure", "azure", 1287, "gpt-4o"),
		modelInfo("gemini-2.5-pro", "gemini", "gemini", 1290, "gemini-2.5-pro"),
	)

	resolver := NewResolverWithRuleProvider(config.FailoverConfig{
		Enabled: true,
		Manual:  map[string][]string{"gpt-4o": {"azure/gpt-4o"}},
	}, registry, &fakeRuleProvider{
		rules: map[string][]string{
			"gpt-4o": {"gemini/gemini-2.5-pro", "azure/gpt-4o"},
		},
	})

	got := resolver.ResolveFailovers(&core.RequestModelResolution{
		Requested:        core.NewRequestedModelSelector("gpt-4o", ""),
		ResolvedSelector: core.ModelSelector{Model: "gpt-4o"},
		ProviderType:     "openai",
	}, core.OperationChatCompletions)

	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2 (dynamic rule wins)", len(got))
	}
	if got[0].QualifiedModel() != "gemini/gemini-2.5-pro" || got[1].QualifiedModel() != "azure/gpt-4o" {
		t.Fatalf("got = [%q,%q], want dynamic targets [gemini/gemini-2.5-pro, azure/gpt-4o]",
			got[0].QualifiedModel(), got[1].QualifiedModel())
	}
}

// A dynamic disabled entry suppresses failover even when a static manual rule
// exists for the same model.
func TestResolverDynamicDisabledSuppressesStaticRule(t *testing.T) {
	registry := newFakeRegistry(
		modelInfo("gpt-4o", "openai", "openai", 1287, "gpt-4o"),
		modelInfo("gpt-4o", "azure", "azure", 1287, "gpt-4o"),
	)

	resolver := NewResolverWithRuleProvider(config.FailoverConfig{
		Enabled: true,
		Manual:  map[string][]string{"gpt-4o": {"azure/gpt-4o"}},
	}, registry, &fakeRuleProvider{
		disabled: map[string]bool{"gpt-4o": true},
	})

	got := resolver.ResolveFailovers(&core.RequestModelResolution{
		Requested:        core.NewRequestedModelSelector("gpt-4o", ""),
		ResolvedSelector: core.ModelSelector{Model: "gpt-4o"},
		ProviderType:     "openai",
	}, core.OperationChatCompletions)

	if len(got) != 0 {
		t.Fatalf("len(got) = %d, want 0 (dynamic disabled suppresses failover)", len(got))
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
						Rank: new(1),
						AsOf: "2026-02-22",
					},
				},
			},
		},
		ProviderName: providerName,
		ProviderType: providerType,
	}
}
