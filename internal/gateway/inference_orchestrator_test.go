package gateway

import (
	"context"
	"io"
	"math"
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

type pricingCaptureResolver struct {
	model    string
	provider string
	pricing  *core.ModelPricing
}

func (r *pricingCaptureResolver) ResolvePricing(model, provider string) *core.ModelPricing {
	r.model = model
	r.provider = provider
	return r.pricing
}

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

func TestExecuteChatCompletionPricesRequestedModelWhenResponseModelIsVersioned(t *testing.T) {
	zero := 0.0
	perRequest := 0.033333
	logger := &usageCaptureLogger{config: usage.Config{Enabled: true}}
	pricing := &pricingCaptureResolver{pricing: &core.ModelPricing{
		InputPerMtok:  &zero,
		OutputPerMtok: &zero,
		PerRequest:    &perRequest,
	}}
	provider := &providerTypeResolverStub{
		chatResponse: &core.ChatResponse{
			ID:       "chatcmpl-test",
			Model:    "gpt-4o-mini-2024-07-18",
			Provider: "openai",
			Usage: core.Usage{
				PromptTokens:     12,
				CompletionTokens: 1,
				TotalTokens:      13,
			},
		},
	}
	orchestrator := NewInferenceOrchestrator(InferenceConfig{
		Provider:        provider,
		UsageLogger:     logger,
		PricingResolver: pricing,
	})
	workflow := &core.Workflow{
		ProviderType: "openai",
		Resolution: &core.RequestModelResolution{
			Requested:        core.NewRequestedModelSelector("openai/gpt-4o-mini", ""),
			ResolvedSelector: core.ModelSelector{Provider: "openai", Model: "gpt-4o-mini"},
			ProviderType:     "openai",
			ProviderName:     "openai",
		},
	}

	_, err := orchestrator.ExecuteChatCompletion(
		context.Background(),
		workflow,
		&core.ChatRequest{Model: "openai/gpt-4o-mini"},
		"req-usage-pricing",
		"/v1/chat/completions",
	)
	if err != nil {
		t.Fatalf("ExecuteChatCompletion() error = %v", err)
	}

	if pricing.model != "gpt-4o-mini" {
		t.Fatalf("pricing model = %q, want gpt-4o-mini", pricing.model)
	}
	if pricing.provider != "openai" {
		t.Fatalf("pricing provider = %q, want openai", pricing.provider)
	}
	if len(logger.entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(logger.entries))
	}
	entry := logger.entries[0]
	if entry.Model != "gpt-4o-mini-2024-07-18" {
		t.Fatalf("usage model = %q, want provider response model", entry.Model)
	}
	if entry.TotalCost == nil || math.Abs(*entry.TotalCost-perRequest) > 0.0000001 {
		t.Fatalf("total cost = %v, want %f", entry.TotalCost, perRequest)
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
				Failover:   true,
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
	chatResponse  *core.ChatResponse
}

func (p *providerTypeResolverStub) ChatCompletion(context.Context, *core.ChatRequest) (*core.ChatResponse, error) {
	return p.chatResponse, nil
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
