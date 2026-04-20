package gateway

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"gomodel/internal/core"
)

// ApplyWorkflowPolicy applies the first matching workflow policy to workflow.
func ApplyWorkflowPolicy(ctx context.Context, workflow *core.Workflow, resolver WorkflowPolicyResolver, selector core.WorkflowSelector) error {
	if workflow == nil || resolver == nil {
		return nil
	}
	policy, err := resolver.Match(selector)
	if err != nil {
		return NormalizeWorkflowPolicyError(err)
	}
	workflow.Policy = policy
	ApplyWorkflowContextOverrides(ctx, workflow)
	return nil
}

// ApplyWorkflowContextOverrides applies request-origin-specific policy changes.
func ApplyWorkflowContextOverrides(ctx context.Context, workflow *core.Workflow) {
	if workflow == nil || ctx == nil {
		return
	}
	if core.GetRequestOrigin(ctx) != core.RequestOriginGuardrail {
		return
	}
	if workflow.Policy == nil {
		return
	}

	cloned := *workflow.Policy
	cloned.Features.Guardrails = false
	cloned.GuardrailsHash = ""
	workflow.Policy = &cloned
}

// NormalizeWorkflowPolicyError converts policy lookup failures into gateway errors.
func NormalizeWorkflowPolicyError(err error) error {
	if err == nil {
		return nil
	}
	if gatewayErr, ok := errors.AsType[*core.GatewayError](err); ok {
		return gatewayErr
	}
	return core.NewProviderError("", http.StatusInternalServerError, "failed to resolve workflow policy", err)
}

// TranslatedWorkflow builds a translated execution workflow for a resolved model.
func TranslatedWorkflow(
	ctx context.Context,
	requestID string,
	endpoint core.EndpointDescriptor,
	resolution *core.RequestModelResolution,
	policyResolver WorkflowPolicyResolver,
) (*core.Workflow, error) {
	workflow := &core.Workflow{
		RequestID:    requestID,
		Endpoint:     endpoint,
		Mode:         core.ExecutionModeTranslated,
		Capabilities: core.CapabilitiesForEndpoint(endpoint),
	}
	if resolution != nil {
		workflow.ProviderType = strings.TrimSpace(resolution.ProviderType)
		workflow.Resolution = resolution
	}

	selector := core.WorkflowSelector{}
	if resolution != nil {
		selector = core.NewWorkflowSelector(
			ResolvedWorkflowProviderName(resolution),
			resolution.ResolvedSelector.Model,
			core.UserPathFromContext(ctx),
		)
	}
	if err := ApplyWorkflowPolicy(ctx, workflow, policyResolver, selector); err != nil {
		return nil, err
	}
	return workflow, nil
}
