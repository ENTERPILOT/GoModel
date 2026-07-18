package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/gateway"
)

type requestWorkflowPolicyResolverFunc func(selector core.WorkflowSelector) (*core.ResolvedWorkflowPolicy, error)

func (f requestWorkflowPolicyResolverFunc) Match(selector core.WorkflowSelector) (*core.ResolvedWorkflowPolicy, error) {
	return f(selector)
}

func TestDeriveWorkflowWithPolicy_RealtimeAndMCPUseUserPathScope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		mode core.ExecutionMode
	}{
		{name: "realtime", path: "/v1/realtime", mode: core.ExecutionModeRealtime},
		{name: "mcp", path: "/mcp/github", mode: core.ExecutionModeMCP},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, tt.path, nil)
			req = req.WithContext(core.WithEffectiveUserPath(req.Context(), "/team/alpha"))
			c := e.NewContext(req, httptest.NewRecorder())
			var matched core.WorkflowSelector
			policy := &core.ResolvedWorkflowPolicy{
				VersionID: "version-1",
				Features:  core.WorkflowFeatures{Audit: true, Usage: false, Budget: false},
			}
			workflow, err := deriveWorkflowWithPolicy(c, nil, nil, requestWorkflowPolicyResolverFunc(func(selector core.WorkflowSelector) (*core.ResolvedWorkflowPolicy, error) {
				matched = selector
				return policy, nil
			}))
			if err != nil {
				t.Fatalf("deriveWorkflowWithPolicy() error = %v", err)
			}
			if workflow == nil || workflow.Mode != tt.mode {
				t.Fatalf("workflow = %+v, want mode %q", workflow, tt.mode)
			}
			if matched.Provider != "" || matched.Model != "" || matched.UserPath != "/team/alpha" {
				t.Fatalf("selector = %+v, want user-path-only selector", matched)
			}
			if workflow.Policy != policy || workflow.UsageEnabled() || workflow.BudgetEnabled() {
				t.Fatalf("matched workflow feature policy was not attached: %+v", workflow.Policy)
			}
		})
	}
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
