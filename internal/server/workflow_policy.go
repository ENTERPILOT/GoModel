package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
)

// RequestWorkflowPolicyResolver matches persisted workflow versions for requests.
type RequestWorkflowPolicyResolver interface {
	Match(selector core.WorkflowSelector) (*core.ResolvedWorkflowPolicy, error)
}

func applyWorkflowPolicy(ctx context.Context, workflow *core.Workflow, resolver RequestWorkflowPolicyResolver, selector core.WorkflowSelector) error {
	if workflow == nil || resolver == nil {
		return nil
	}
	policy, err := resolver.Match(selector)
	if err != nil {
		return normalizeWorkflowPolicyError(err)
	}
	workflow.Policy = policy
	applyWorkflowContextOverrides(ctx, workflow)
	return nil
}

func applyWorkflowContextOverrides(ctx context.Context, workflow *core.Workflow) {
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

func normalizeWorkflowPolicyError(err error) error {
	if err == nil {
		return nil
	}
	if gatewayErr, ok := errors.AsType[*core.GatewayError](err); ok {
		return gatewayErr
	}
	return core.NewProviderError("", http.StatusInternalServerError, "failed to resolve workflow policy", err)
}

func cloneCurrentWorkflow(c *echo.Context) *core.Workflow {
	if c == nil {
		return nil
	}
	if existing := core.GetWorkflow(c.Request().Context()); existing != nil {
		cloned := *existing
		return &cloned
	}
	return &core.Workflow{}
}

func workflowVersionID(workflow *core.Workflow) string {
	if workflow == nil {
		return ""
	}
	return workflow.WorkflowVersionID()
}

func boolPtr(value bool) *bool {
	return &value
}
