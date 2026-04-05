package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
)

// RequestExecutionPolicyResolver matches persisted execution-plan versions for requests.
type RequestExecutionPolicyResolver interface {
	Match(selector core.ExecutionPlanSelector) (*core.ResolvedExecutionPolicy, error)
}

func applyExecutionPolicy(ctx context.Context, plan *core.ExecutionPlan, resolver RequestExecutionPolicyResolver, selector core.ExecutionPlanSelector) error {
	if plan == nil || resolver == nil {
		return nil
	}
	policy, err := resolver.Match(selector)
	if err != nil {
		return normalizeExecutionPolicyError(err)
	}
	plan.Policy = policy
	applyExecutionContextOverrides(ctx, plan)
	return nil
}

func applyExecutionContextOverrides(ctx context.Context, plan *core.ExecutionPlan) {
	if plan == nil || ctx == nil {
		return
	}
	if core.GetRequestOrigin(ctx) != core.RequestOriginGuardrail {
		return
	}
	if plan.Policy == nil {
		return
	}

	cloned := *plan.Policy
	cloned.Features.Guardrails = false
	cloned.GuardrailsHash = ""
	plan.Policy = &cloned
}

func normalizeExecutionPolicyError(err error) error {
	if err == nil {
		return nil
	}
	if gatewayErr, ok := errors.AsType[*core.GatewayError](err); ok {
		return gatewayErr
	}
	return core.NewProviderError("", http.StatusInternalServerError, "failed to resolve execution policy", err)
}

func cloneCurrentExecutionPlan(c *echo.Context) *core.ExecutionPlan {
	if c == nil {
		return nil
	}
	if existing := core.GetExecutionPlan(c.Request().Context()); existing != nil {
		cloned := *existing
		return &cloned
	}
	return &core.ExecutionPlan{}
}

func executionPlanVersionID(plan *core.ExecutionPlan) string {
	if plan == nil {
		return ""
	}
	return plan.ExecutionPlanVersionID()
}

func boolPtr(value bool) *bool {
	return &value
}
