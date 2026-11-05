package gateway

import (
	"context"
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/usage"
	"github.com/stretchr/testify/require"
)

type embeddingsExecutionProvider struct {
	core.RoutableProvider
	responses map[string]*core.EmbeddingResponse
	errors    map[string]error
}

func (p *embeddingsExecutionProvider) Embeddings(_ context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	key := req.Model
	if req.Provider != "" {
		key = req.Provider + "/" + req.Model
	}
	return p.responses[key], p.errors[key]
}

func (*embeddingsExecutionProvider) Supports(string) bool { return true }

func (*embeddingsExecutionProvider) GetProviderType(string) string { return "openai" }

func (*embeddingsExecutionProvider) GetProviderName(model string) string {
	selector, err := core.ParseModelSelector(model, "")
	if err != nil {
		return ""
	}
	return selector.Provider
}

func TestExecuteEmbeddingsFailsOverAndReportsTheSelectedRoute(t *testing.T) {
	primary := "primary-openai/text-embedding-3-small"
	backup := "backup-openai/text-embedding-3-small"
	provider := &embeddingsExecutionProvider{
		responses: map[string]*core.EmbeddingResponse{
			backup: {Model: "text-embedding-3-small-v2", Provider: "openai", Usage: core.EmbeddingUsage{PromptTokens: 2, TotalTokens: 2}},
		},
		errors: map[string]error{
			primary: core.NewProviderError("openai", http.StatusServiceUnavailable, "primary unavailable", nil),
		},
	}
	logger := &usageCaptureLogger{config: usage.Config{Enabled: true}}
	pricing := &pricingCaptureResolver{}
	orchestrator := NewInferenceOrchestrator(InferenceConfig{
		Provider:         provider,
		FailoverResolver: stubFailoverResolver{selectors: []core.ModelSelector{{Provider: "backup-openai", Model: "text-embedding-3-small"}}},
		UsageLogger:      logger,
		PricingResolver:  pricing,
	})
	workflow := &core.Workflow{
		Endpoint:     core.EndpointDescriptor{Operation: core.OperationEmbeddings},
		ProviderType: "openai",
		Resolution: &core.RequestModelResolution{
			ResolvedSelector: core.ModelSelector{Provider: "primary-openai", Model: "text-embedding-3-small"},
			ProviderType:     "openai",
			ProviderName:     "primary-openai",
		},
		Policy: &core.ResolvedWorkflowPolicy{
			VersionID: "embeddings-failover",
			Features:  core.WorkflowFeatures{Failover: true, Usage: true},
		},
	}
	ctx := WithAttemptRecorder(context.Background())

	result, err := orchestrator.ExecuteEmbeddings(ctx, workflow,
		&core.EmbeddingRequest{Model: "text-embedding-3-small", Provider: "primary-openai"},
		"request-id", "/v1/embeddings")

	require.NoError(t, err)
	require.Equal(t, "text-embedding-3-small-v2", result.Meta.Model)
	require.Equal(t, "openai", result.Meta.ProviderType)
	require.Equal(t, "backup-openai", result.Meta.ProviderName)
	require.Equal(t, backup, result.Meta.FailoverModel)
	require.True(t, result.Meta.UsedFailover)
	require.Equal(t, "backup-openai", result.Response.Provider)
	require.Equal(t, backup, pricing.model)
	require.Equal(t, "backup-openai", pricing.provider)
	require.Len(t, logger.entries, 1)
	require.Equal(t, "text-embedding-3-small-v2", logger.entries[0].Model)
	require.Equal(t, "openai", logger.entries[0].Provider)
	require.Equal(t, "backup-openai", logger.entries[0].ProviderName)
	require.Equal(t, backup, logger.entries[0].RoutedModel)

	attempts := AttemptsFromContext(ctx)
	require.Len(t, attempts, 2)
	require.Equal(t, AttemptKindPrimary, attempts[0].Kind)
	require.Equal(t, "openai", attempts[0].ProviderType)
	require.Equal(t, "primary-openai", attempts[0].ProviderName)
	require.Equal(t, primary, attempts[0].Model)
	require.Equal(t, AttemptKindFailover, attempts[1].Kind)
	require.Equal(t, "openai", attempts[1].ProviderType)
	require.Equal(t, "backup-openai", attempts[1].ProviderName)
	require.Equal(t, backup, attempts[1].Model)

	// The provider fixture reuses response pointers; restore its upstream type
	// after the preceding execution named the client-facing response.
	provider.responses[backup].Provider = "openai"
	dispatched, providerType, providerName, err := orchestrator.DispatchEmbeddings(ctx, workflow,
		&core.EmbeddingRequest{Model: "text-embedding-3-small", Provider: "primary-openai"})
	require.NoError(t, err)
	require.Equal(t, "backup-openai", dispatched.Provider)
	require.Equal(t, "openai", providerType)
	require.Equal(t, "backup-openai", providerName)
	require.Len(t, logger.entries, 1, "dispatch must not record usage")
}
