package server

import (
	"context"
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
)

func ensureTranslatedRequestPlan(
	c *echo.Context,
	provider core.RoutableProvider,
	resolver RequestModelResolver,
	policyResolver RequestExecutionPolicyResolver,
	model,
	providerHint *string,
) (*core.ExecutionPlan, error) {
	if model == nil || providerHint == nil {
		return nil, core.NewInvalidRequestError("model selector targets are required", nil)
	}

	plan, err := ensureTranslatedExecutionPlan(c, provider, resolver, policyResolver)
	if err != nil {
		return nil, err
	}

	resolution := translatedPlanResolution(plan)
	if resolution == nil {
		resolution, err = resolveAndStoreRequestModelResolution(c, provider, resolver, *model, *providerHint)
		if err != nil {
			return nil, err
		}
		plan, err = translatedExecutionPlanForRequest(c, resolution, policyResolver)
		if err != nil {
			return nil, err
		}
		storeExecutionPlan(c, plan)
	}

	applyResolvedSelector(model, providerHint, resolution)
	return plan, nil
}

func ensureTranslatedExecutionPlan(
	c *echo.Context,
	provider core.RoutableProvider,
	resolver RequestModelResolver,
	policyResolver RequestExecutionPolicyResolver,
) (*core.ExecutionPlan, error) {
	if plan := currentTranslatedExecutionPlan(c); plan != nil {
		return plan, nil
	}

	plan, err := deriveExecutionPlanWithPolicy(c, provider, resolver, policyResolver)
	if err != nil || plan == nil {
		return plan, err
	}

	storeExecutionPlan(c, plan)
	return core.GetExecutionPlan(c.Request().Context()), nil
}

func currentTranslatedExecutionPlan(c *echo.Context) *core.ExecutionPlan {
	if c == nil {
		return nil
	}
	plan := core.GetExecutionPlan(c.Request().Context())
	if plan == nil {
		return nil
	}

	desc := core.DescribeEndpoint(c.Request().Method, c.Request().URL.Path)
	if plan.Mode != core.ExecutionModeTranslated || plan.Endpoint.Operation != desc.Operation {
		return nil
	}
	return plan
}

func translatedPlanResolution(plan *core.ExecutionPlan) *core.RequestModelResolution {
	if plan == nil {
		return nil
	}
	return plan.Resolution
}

func applyResolvedSelector(model, providerHint *string, resolution *core.RequestModelResolution) {
	if model == nil || providerHint == nil || resolution == nil {
		return
	}
	*model = resolution.ResolvedSelector.Model
	*providerHint = resolution.ResolvedSelector.Provider
}

func translatedExecutionPlanForRequest(
	c *echo.Context,
	resolution *core.RequestModelResolution,
	policyResolver RequestExecutionPolicyResolver,
) (*core.ExecutionPlan, error) {
	if c == nil {
		return nil, nil
	}

	requestID := requestIDFromContextOrHeader(c.Request())
	ctx := c.Request().Context()
	if requestID != "" && strings.TrimSpace(core.GetRequestID(ctx)) != requestID {
		ctx = core.WithRequestID(ctx, requestID)
		c.SetRequest(c.Request().WithContext(ctx))
	}

	return translatedExecutionPlan(
		c.Request().Context(),
		requestID,
		core.DescribeEndpoint(c.Request().Method, c.Request().URL.Path),
		resolution,
		policyResolver,
	)
}

func translatedExecutionPlan(
	ctx context.Context,
	requestID string,
	endpoint core.EndpointDescriptor,
	resolution *core.RequestModelResolution,
	policyResolver RequestExecutionPolicyResolver,
) (*core.ExecutionPlan, error) {
	plan := &core.ExecutionPlan{
		RequestID:    strings.TrimSpace(requestID),
		Endpoint:     endpoint,
		Mode:         core.ExecutionModeTranslated,
		Capabilities: core.CapabilitiesForEndpoint(endpoint),
	}
	if resolution != nil {
		plan.ProviderType = strings.TrimSpace(resolution.ProviderType)
		plan.Resolution = resolution
	}

	selector := core.ExecutionPlanSelector{}
	if resolution != nil {
		selector = core.NewExecutionPlanSelector(
			resolvedWorkflowProviderName(resolution),
			resolution.ResolvedSelector.Model,
			core.UserPathFromContext(ctx),
		)
	}
	if err := applyExecutionPolicy(ctx, plan, policyResolver, selector); err != nil {
		return nil, err
	}
	return plan, nil
}
