package gateway

import (
	"context"
	"io"
	"testing"

	"gomodel/internal/core"
	"gomodel/internal/usage"
)

type usageCaptureLogger struct {
	config  usage.Config
	entries []*usage.UsageEntry
}

func (l *usageCaptureLogger) Write(entry *usage.UsageEntry) {
	l.entries = append(l.entries, entry)
}

func (l *usageCaptureLogger) Config() usage.Config { return l.config }
func (l *usageCaptureLogger) Close() error         { return nil }

func TestInferenceOrchestratorLogUsageAssignsUserPathAndProviderName(t *testing.T) {
	logger := &usageCaptureLogger{config: usage.Config{Enabled: true}}
	orchestrator := NewInferenceOrchestrator(InferenceConfig{UsageLogger: logger})
	ctx := core.WithRequestSnapshot(context.Background(), &core.RequestSnapshot{UserPath: "/team/alpha"})

	orchestrator.LogUsage(ctx, nil, "gpt-5-nano", "openai", "primary-openai", func(*core.ModelPricing) *usage.UsageEntry {
		return &usage.UsageEntry{ID: "usage-1"}
	})

	if len(logger.entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(logger.entries))
	}
	if got := logger.entries[0].UserPath; got != "/team/alpha" {
		t.Fatalf("UserPath = %q, want /team/alpha", got)
	}
	if got := logger.entries[0].ProviderName; got != "primary-openai" {
		t.Fatalf("ProviderName = %q, want primary-openai", got)
	}
}

func TestInferenceOrchestratorLogUsageSkipsWhenWorkflowDisablesUsage(t *testing.T) {
	logger := &usageCaptureLogger{config: usage.Config{Enabled: true}}
	orchestrator := NewInferenceOrchestrator(InferenceConfig{UsageLogger: logger})

	orchestrator.LogUsage(context.Background(), &core.Workflow{
		Policy: &core.ResolvedWorkflowPolicy{
			VersionID: "workflow-usage-off",
			Features: core.WorkflowFeatures{
				Cache:      true,
				Audit:      true,
				Usage:      false,
				Guardrails: true,
			},
		},
	}, "gpt-5-nano", "openai", "primary-openai", func(*core.ModelPricing) *usage.UsageEntry {
		return &usage.UsageEntry{ID: "usage-1"}
	})

	if len(logger.entries) != 0 {
		t.Fatalf("len(entries) = %d, want 0", len(logger.entries))
	}
}

func TestInferenceOrchestratorWithCacheRequestContextClearsInheritedGuardrailsHash(t *testing.T) {
	orchestrator := NewInferenceOrchestrator(InferenceConfig{GuardrailsHash: "service-default"})
	ctx := core.WithGuardrailsHash(context.Background(), "caller-hash")
	workflow := &core.Workflow{
		Policy: &core.ResolvedWorkflowPolicy{
			VersionID:      "workflow-1",
			GuardrailsHash: "",
			Features: core.WorkflowFeatures{
				Cache:      true,
				Audit:      true,
				Usage:      true,
				Guardrails: false,
				Fallback:   true,
			},
		},
	}

	got := orchestrator.WithCacheRequestContext(ctx, workflow)
	if hash := core.GetGuardrailsHash(got); hash != "" {
		t.Fatalf("guardrails hash = %q, want cleared hash", hash)
	}
}

func TestInferenceOrchestratorProviderTypeForSelectorPrefersExplicitProvider(t *testing.T) {
	orchestrator := NewInferenceOrchestrator(InferenceConfig{Provider: &providerTypeResolverStub{}})

	got := orchestrator.ProviderTypeForSelector(core.ModelSelector{Provider: "azure", Model: "gpt-4o"}, "openai")
	if got != "azure" {
		t.Fatalf("ProviderTypeForSelector() = %q, want azure", got)
	}
}

func TestInferenceOrchestratorProviderTypeForSelectorCanonicalizesProviderNameSelectors(t *testing.T) {
	orchestrator := NewInferenceOrchestrator(InferenceConfig{
		Provider: &providerTypeResolverStub{
			providerTypes: map[string]string{
				"openai_test/gpt-4o": "openai",
			},
		},
	})

	got := orchestrator.ProviderTypeForSelector(core.ModelSelector{Provider: "openai_test", Model: "gpt-4o"}, "anthropic")
	if got != "openai" {
		t.Fatalf("ProviderTypeForSelector() = %q, want openai", got)
	}
}

func TestQualifyModelWithProviderPrefixesSlashModelIDs(t *testing.T) {
	got := QualifyModelWithProvider("openai/gpt-4o-mini", "openrouter")
	if got != "openrouter/openai/gpt-4o-mini" {
		t.Fatalf("QualifyModelWithProvider() = %q, want openrouter/openai/gpt-4o-mini", got)
	}
}

func TestQualifyModelWithProviderKeepsAlreadyQualifiedModelIDs(t *testing.T) {
	got := QualifyModelWithProvider("openrouter/openai/gpt-4o-mini", "openrouter")
	if got != "openrouter/openai/gpt-4o-mini" {
		t.Fatalf("QualifyModelWithProvider() = %q, want unchanged model", got)
	}
}

type providerTypeResolverStub struct {
	providerTypes map[string]string
}

func (p *providerTypeResolverStub) ChatCompletion(context.Context, *core.ChatRequest) (*core.ChatResponse, error) {
	return nil, nil
}

func (p *providerTypeResolverStub) StreamChatCompletion(context.Context, *core.ChatRequest) (io.ReadCloser, error) {
	return nil, nil
}

func (p *providerTypeResolverStub) ListModels(context.Context) (*core.ModelsResponse, error) {
	return nil, nil
}

func (p *providerTypeResolverStub) Responses(context.Context, *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return nil, nil
}

func (p *providerTypeResolverStub) StreamResponses(context.Context, *core.ResponsesRequest) (io.ReadCloser, error) {
	return nil, nil
}

func (p *providerTypeResolverStub) Embeddings(context.Context, *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, nil
}

func (p *providerTypeResolverStub) Supports(string) bool { return true }

func (p *providerTypeResolverStub) GetProviderType(model string) string {
	return p.providerTypes[model]
}
