package server

import (
	"context"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
	"gomodel/internal/gateway"
)

// RequestModelResolver resolves raw request selectors into concrete model
// selectors before provider execution.
type RequestModelResolver = gateway.ModelResolver

// RequestFallbackResolver resolves alternate concrete model selectors for a
// translated request after the primary selector has already been resolved.
type RequestFallbackResolver = gateway.FallbackResolver

func workflowProviderNameForType(provider core.RoutableProvider, providerType string) string {
	return gateway.WorkflowProviderNameForType(provider, providerType)
}

func resolveRequestModel(provider core.RoutableProvider, resolver RequestModelResolver, requested core.RequestedModelSelector) (*core.RequestModelResolution, error) {
	return gateway.ResolveRequestModel(provider, resolver, requested)
}

func resolveRequestModelWithAuthorizer(
	ctx context.Context,
	provider core.RoutableProvider,
	resolver RequestModelResolver,
	authorizer RequestModelAuthorizer,
	requested core.RequestedModelSelector,
) (*core.RequestModelResolution, error) {
	return gateway.ResolveRequestModelWithAuthorizer(ctx, provider, resolver, authorizer, requested)
}

func storeRequestModelResolution(c *echo.Context, resolution *core.RequestModelResolution) {
	if c == nil || resolution == nil {
		return
	}

	ctx := c.Request().Context()
	if workflow := core.GetWorkflow(ctx); workflow != nil {
		cloned := *workflow
		cloned.ProviderType = resolution.ProviderType
		cloned.Resolution = resolution
		auditlog.EnrichEntryWithWorkflow(c, &cloned)
		ctx = core.WithWorkflow(ctx, &cloned)
	}
	if env := core.GetWhiteBoxPrompt(ctx); env != nil {
		env.RouteHints.Model = resolution.ResolvedSelector.Model
		env.RouteHints.Provider = resolution.ResolvedSelector.Provider
	}
	c.SetRequest(c.Request().WithContext(ctx))
}

func ensureRequestModelResolution(c *echo.Context, provider core.RoutableProvider, resolver RequestModelResolver) (*core.RequestModelResolution, bool, error) {
	if c == nil {
		return nil, false, nil
	}
	if resolution := currentRequestModelResolution(c); resolution != nil {
		return resolution, true, nil
	}

	model, providerHint, parsed, err := selectorHintsForValidation(c)
	if err != nil || !parsed {
		return nil, parsed, err
	}
	resolution, err := resolveAndStoreRequestModelResolution(c, provider, resolver, nil, model, providerHint)
	return resolution, true, err
}

func currentRequestModelResolution(c *echo.Context) *core.RequestModelResolution {
	if c == nil {
		return nil
	}
	if workflow := core.GetWorkflow(c.Request().Context()); workflow != nil {
		return workflow.Resolution
	}
	return nil
}

func resolveAndStoreRequestModelResolution(
	c *echo.Context,
	provider core.RoutableProvider,
	resolver RequestModelResolver,
	authorizer RequestModelAuthorizer,
	model, providerHint string,
) (*core.RequestModelResolution, error) {
	requested := core.NewRequestedModelSelector(model, providerHint)
	enrichAuditEntryWithRequestedModel(c, requested)

	resolution, err := resolveRequestModelWithAuthorizer(c.Request().Context(), provider, resolver, authorizer, requested)
	if err != nil {
		return nil, err
	}
	storeRequestModelResolution(c, resolution)
	return resolution, nil
}

func enrichAuditEntryWithRequestedModel(c *echo.Context, requested core.RequestedModelSelector) {
	if c == nil {
		return
	}
	requested = core.NewRequestedModelSelector(requested.Model, requested.ProviderHint)
	if requested.Model == "" {
		return
	}
	workflow := &core.Workflow{}
	if existing := core.GetWorkflow(c.Request().Context()); existing != nil {
		cloned := *existing
		workflow = &cloned
	}
	workflow.Resolution = &core.RequestModelResolution{
		Requested: requested,
	}
	auditlog.EnrichEntryWithWorkflow(c, workflow)
}
