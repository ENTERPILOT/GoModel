package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"gomodel/internal/core"
	"gomodel/internal/gateway"
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
	if err == nil {
		t.Fatal("applyWorkflowPolicy() error = nil, want gateway error")
	}

	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("applyWorkflowPolicy() error = %T, want *core.GatewayError", err)
	}
	if gatewayErr.Type != core.ErrorTypeProvider {
		t.Fatalf("gateway error type = %q, want %q", gatewayErr.Type, core.ErrorTypeProvider)
	}
	if gatewayErr.HTTPStatusCode() != http.StatusInternalServerError {
		t.Fatalf("gateway error status = %d, want %d", gatewayErr.HTTPStatusCode(), http.StatusInternalServerError)
	}
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
	if err != nil {
		t.Fatalf("DetermineBatchExecutionSelectionWithAuthorizerAndInputFileResolver() error = %v", err)
	}
	if selection.ProviderType != "openai" {
		t.Fatalf("providerType = %q, want openai", selection.ProviderType)
	}
	if selection.Selector.Provider != "openai" || selection.Selector.Model != "gpt-4o-mini" {
		t.Fatalf("selector = %+v, want openai/gpt-4o-mini", selection.Selector)
	}
	if resolver.calls != len(req.Requests) {
		t.Fatalf("resolver calls = %d, want %d", resolver.calls, len(req.Requests))
	}
}
