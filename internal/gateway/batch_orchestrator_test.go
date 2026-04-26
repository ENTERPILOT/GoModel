package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"gomodel/internal/core"
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
	if err == nil {
		t.Fatal("workflowForBatch() error = nil, want gateway error")
	}

	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("workflowForBatch() error = %T, want *core.GatewayError", err)
	}
	if gatewayErr.Type != core.ErrorTypeProvider {
		t.Fatalf("gateway error type = %q, want %q", gatewayErr.Type, core.ErrorTypeProvider)
	}
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
			if selector.Provider != "openai" {
				t.Fatalf("workflow selector provider = %q, want openai", selector.Provider)
			}
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
	if !errors.Is(err, budgetErr) {
		t.Fatalf("Create() error = %v, want %v", err, budgetErr)
	}
	if budgetWorkflow == nil || budgetWorkflow.Policy == nil {
		t.Fatal("budget enforcer did not receive resolved workflow")
	}
	if budgetWorkflow.Policy.VersionID != "workflow-budget-disabled" {
		t.Fatalf("budget workflow version = %q, want workflow-budget-disabled", budgetWorkflow.Policy.VersionID)
	}
	if budgetRequestID != "req-budget" {
		t.Fatalf("budget request id = %q, want req-budget", budgetRequestID)
	}
	if provider.createCalls != 0 {
		t.Fatalf("provider CreateBatch calls = %d, want 0", provider.createCalls)
	}
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
