package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
)

type workflowPolicyResolverFunc func(selector core.WorkflowSelector) (*core.ResolvedWorkflowPolicy, error)

func (f workflowPolicyResolverFunc) Match(selector core.WorkflowSelector) (*core.ResolvedWorkflowPolicy, error) {
	return f(selector)
}

func TestBatchOrchestratorWorkflowForBatchNormalizesPolicyErrors(t *testing.T) {
	t.Parallel()

	orchestrator := NewBatchOrchestrator(BatchConfig{
		WorkflowPolicyResolver: workflowPolicyResolverFunc(func(core.WorkflowSelector) (*core.ResolvedWorkflowPolicy, error) {
			return nil, errors.New("resolver backend unavailable")
		}),
	})

	_, err := orchestrator.workflowForBatch(context.Background(), BatchMeta{
		RequestID: "req-1",
		Endpoint:  core.DescribeEndpoint(http.MethodPost, "/v1/batches"),
	}, BatchExecutionSelection{
		ProviderType: "openai",
		Selector:     core.NewWorkflowSelector("openai", "gpt-4o-mini"),
	})
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, core.ErrorTypeProvider, gatewayErr.Type)
}

func TestBatchOrchestratorCreateEnforcesBudgetAfterWorkflowResolution(t *testing.T) {
	t.Parallel()

	provider := &batchBudgetProvider{}
	budgetErr := errors.New("budget denied")
	var budgetWorkflow *core.Workflow
	var budgetRequestID string

	orchestrator := NewBatchOrchestrator(BatchConfig{
		Provider: provider,
		WorkflowPolicyResolver: workflowPolicyResolverFunc(func(selector core.WorkflowSelector) (*core.ResolvedWorkflowPolicy, error) {
			require.Equal(t, "openai", selector.Provider)

			return &core.ResolvedWorkflowPolicy{
				VersionID: "workflow-budget-disabled",
				Features: core.WorkflowFeatures{
					Usage:  true,
					Budget: false,
				},
			}, nil
		}),
		BudgetEnforcer: func(ctx context.Context) error {
			budgetWorkflow = core.GetWorkflow(ctx)
			budgetRequestID = core.GetRequestID(ctx)
			return budgetErr
		},
	})

	_, err := orchestrator.Create(context.Background(), &core.BatchRequest{
		InputFileID: "file-123",
		Endpoint:    "/v1/chat/completions",
		Metadata: map[string]string{
			"provider": "openai",
		},
	}, BatchMeta{
		RequestID: "req-budget",
		Endpoint:  core.DescribeEndpoint(http.MethodPost, "/v1/batches"),
	})
	require.ErrorIs(t, err, budgetErr)
	require.NotNil(t, budgetWorkflow)
	require.NotNil(t, budgetWorkflow.Policy)
	require.Equal(t, "workflow-budget-disabled", budgetWorkflow.Policy.VersionID)
	require.Equal(t, "req-budget", budgetRequestID)
	require.Equal(t, 0, provider.createCalls)
}

type batchBudgetProvider struct {
	createCalls int
}

func (p *batchBudgetProvider) Supports(string) bool { return true }

func (p *batchBudgetProvider) GetProviderType(string) string { return "openai" }

func (p *batchBudgetProvider) GetProviderNameForType(providerType string) string { return providerType }

func (p *batchBudgetProvider) ChatCompletion(context.Context, *core.ChatRequest) (*core.ChatResponse, error) {
	return nil, nil
}

func (p *batchBudgetProvider) StreamChatCompletion(context.Context, *core.ChatRequest) (io.ReadCloser, error) {
	return nil, nil
}

func (p *batchBudgetProvider) ListModels(context.Context) (*core.ModelsResponse, error) {
	return nil, nil
}

func (p *batchBudgetProvider) Responses(context.Context, *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return nil, nil
}

func (p *batchBudgetProvider) StreamResponses(context.Context, *core.ResponsesRequest) (io.ReadCloser, error) {
	return nil, nil
}

func (p *batchBudgetProvider) Embeddings(context.Context, *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, nil
}

func (p *batchBudgetProvider) CreateBatch(_ context.Context, _ string, req *core.BatchRequest) (*core.BatchResponse, error) {
	p.createCalls++
	return &core.BatchResponse{
		ID:        "provider-batch-123",
		Endpoint:  req.Endpoint,
		Status:    "validating",
		CreatedAt: 1,
	}, nil
}

func (p *batchBudgetProvider) GetBatch(context.Context, string, string) (*core.BatchResponse, error) {
	return nil, nil
}

func (p *batchBudgetProvider) ListBatches(context.Context, string, int, string) (*core.BatchListResponse, error) {
	return nil, nil
}

func (p *batchBudgetProvider) CancelBatch(context.Context, string, string) (*core.BatchResponse, error) {
	return nil, nil
}

func (p *batchBudgetProvider) GetBatchResults(context.Context, string, string) (*core.BatchResultsResponse, error) {
	return nil, nil
}
