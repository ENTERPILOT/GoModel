package server

import (
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
)

func ensureTranslatedRequestPlan(
	c *echo.Context,
	provider core.RoutableProvider,
	model,
	providerHint *string,
) (*core.ExecutionPlan, error) {
	if model == nil || providerHint == nil {
		return nil, core.NewInvalidRequestError("model selector targets are required", nil)
	}

	plan, err := ensureTranslatedExecutionPlan(c, provider)
	if err != nil {
		return nil, err
	}

	resolution := translatedPlanResolution(plan)
	if resolution == nil {
		resolution, err = resolveAndStoreRequestModelResolution(c, provider, *model, *providerHint)
		if err != nil {
			return nil, err
		}
		plan = translatedExecutionPlanForRequest(c, resolution)
		storeExecutionPlan(c, plan)
	}

	applyResolvedSelector(model, providerHint, resolution)
	return plan, nil
}

func ensureTranslatedExecutionPlan(c *echo.Context, provider core.RoutableProvider) (*core.ExecutionPlan, error) {
	if plan := currentTranslatedExecutionPlan(c); plan != nil {
		return plan, nil
	}

	plan, err := deriveExecutionPlan(c, provider)
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
) *core.ExecutionPlan {
	if c == nil {
		return nil
	}

	requestID := requestIDFromContextOrHeader(c.Request())
	ctx := c.Request().Context()
	if requestID != "" && strings.TrimSpace(core.GetRequestID(ctx)) != requestID {
		ctx = core.WithRequestID(ctx, requestID)
		c.SetRequest(c.Request().WithContext(ctx))
	}

	desc := core.DescribeEndpoint(c.Request().Method, c.Request().URL.Path)

	plan := core.GetExecutionPlan(c.Request().Context())
	if plan != nil {
		cloned := *plan
		plan = &cloned
	} else {
		plan = &core.ExecutionPlan{}
	}

	if requestID != "" {
		plan.RequestID = requestID
	}
	plan.Endpoint = desc
	plan.Mode = core.ExecutionModeTranslated
	plan.Capabilities = core.CapabilitiesForEndpoint(desc)
	plan.ProviderType = strings.TrimSpace(resolution.ProviderType)
	plan.Resolution = resolution

	return plan
}
