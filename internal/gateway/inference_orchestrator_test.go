package gateway

import (
	"context"
	"io"
	"math"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/usage"
	"github.com/stretchr/testify/require"
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

	require.Len(t, logger.entries, 1)
	got := logger.entries[0].UserPath
	require.Equal(t, "/team/alpha", got)
	got = logger.entries[0].ProviderName
	require.Equal(t, "primary-openai", got)
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
			Choices:  []core.Choice{{FinishReason: "stop"}},
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
	require.NoError(t, err)
	require.Equal(t, "gpt-4o-mini", pricing.model)
	require.Equal(t, "openai", pricing.provider)
	require.Len(t, logger.entries, 1)

	entry := logger.entries[0]
	require.Equal(t, "gpt-4o-mini-2024-07-18", entry.Model)
	require.NotNil(t, entry.TotalCost)
	require.LessOrEqual(t, math.Abs(*entry.TotalCost-perRequest), 0.0000001, "total cost = %v, want %f", entry.TotalCost, perRequest)
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

	require.Empty(t, logger.entries)
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
	hash := core.GetGuardrailsHash(got)
	require.Empty(t, hash)
}

func TestInferenceOrchestratorProviderTypeForSelectorPrefersExplicitProvider(t *testing.T) {
	orchestrator := NewInferenceOrchestrator(InferenceConfig{Provider: &providerTypeResolverStub{}})

	got := orchestrator.ProviderTypeForSelector(core.ModelSelector{Provider: "azure", Model: "gpt-4o"}, "openai")
	require.Equal(t, "azure", got)
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
	require.Equal(t, "openai", got)
}

func TestQualifyModelWithProviderPrefixesSlashModelIDs(t *testing.T) {
	got := QualifyModelWithProvider("openai/gpt-4o-mini", "openrouter")
	require.Equal(t, "openrouter/openai/gpt-4o-mini", got)
}

func TestQualifyModelWithProviderKeepsAlreadyQualifiedModelIDs(t *testing.T) {
	got := QualifyModelWithProvider("openrouter/openai/gpt-4o-mini", "openrouter")
	require.Equal(t, "openrouter/openai/gpt-4o-mini", got)
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
