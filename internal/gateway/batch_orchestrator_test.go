package gateway

import (
	"context"
	"errors"
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
