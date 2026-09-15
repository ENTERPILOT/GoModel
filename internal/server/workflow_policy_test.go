package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/gateway"
)

type requestWorkflowPolicyResolverFunc func(selector core.WorkflowSelector) (*core.ResolvedWorkflowPolicy, error)

func (f requestWorkflowPolicyResolverFunc) Match(selector core.WorkflowSelector) (*core.ResolvedWorkflowPolicy, error) {
	return f(selector)
}

type countingBatchResolver struct {
	calls    int
	resolved core.ModelSelector
}

func (r *countingBatchResolver) ResolveModel(requested core.RequestedModelSelector) (core.ModelSelector, bool, error) {
	r.calls++
	return r.resolved, false, nil
}

func TestApplyWorkflowPolicy_NormalizesResolverErrors(t *testing.T) {
	t.Parallel()

	workflow := &core.Workflow{}
	err := applyWorkflowPolicy(context.Background(), workflow, requestWorkflowPolicyResolverFunc(func(core.WorkflowSelector) (*core.ResolvedWorkflowPolicy, error) {
		return nil, errors.New("storage unavailable")
	}), core.NewWorkflowSelector("openai", "gpt-4o-mini"))
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, core.ErrorTypeProvider, gatewayErr.Type)
	require.Equal(t, http.StatusInternalServerError, gatewayErr.HTTPStatusCode())
}

func TestDetermineBatchExecutionSelection_UsesSingleResolutionPass(t *testing.T) {
	t.Parallel()

	provider := &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		providerTypes:   map[string]string{"openai/gpt-4o-mini": "openai"},
	}
	resolver := &countingBatchResolver{
		resolved: core.ModelSelector{Provider: "openai", Model: "gpt-4o-mini"},
	}
	req := &core.BatchRequest{
		Endpoint: "/v1/chat/completions",
		Requests: []core.BatchRequestItem{
			{
				Method: http.MethodPost,
				Body:   json.RawMessage(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`),
			},
			{
				Method: http.MethodPost,
				Body:   json.RawMessage(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}`),
			},
		},
	}

	selection, err := gateway.DetermineBatchExecutionSelectionWithAuthorizerAndInputFileResolver(context.Background(), provider, resolver, nil, nil, req)
	require.NoError(t, err)
	require.Equal(t, "openai", selection.ProviderType)
	require.Equal(t, "openai", selection.Selector.Provider)
	require.Equal(t, "gpt-4o-mini", selection.Selector.Model, "selector = %+v, want openai/gpt-4o-mini", selection.Selector)
	require.Equal(t, len(req.Requests), resolver.calls)
}
