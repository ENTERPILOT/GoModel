package server

import (
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
)

// RequestModelResolver resolves raw request selectors into concrete model
// selectors before provider execution.
type RequestModelResolver interface {
	ResolveModel(model, provider string) (core.ModelSelector, bool, error)
}

func effectiveRequestModelResolver(provider core.RoutableProvider, resolver RequestModelResolver) RequestModelResolver {
	if resolver != nil {
		return resolver
	}
	if providerResolver, ok := provider.(RequestModelResolver); ok {
		return providerResolver
	}
	return nil
}

func resolveRequestModel(provider core.RoutableProvider, resolver RequestModelResolver, model, providerHint string) (*core.RequestModelResolution, error) {
	model = strings.TrimSpace(model)
	providerHint = strings.TrimSpace(providerHint)

	var (
		resolvedSelector core.ModelSelector
		aliasApplied     bool
		err              error
	)

	if effectiveResolver := effectiveRequestModelResolver(provider, resolver); effectiveResolver != nil {
		resolvedSelector, aliasApplied, err = effectiveResolver.ResolveModel(model, providerHint)
	} else {
		resolvedSelector, err = core.ParseModelSelector(model, providerHint)
	}
	if err != nil {
		return nil, core.NewInvalidRequestError(err.Error(), err)
	}

	resolvedModel := resolvedSelector.QualifiedModel()
	if counted, ok := provider.(modelCountProvider); ok && counted.ModelCount() == 0 {
		return nil, core.NewProviderError("", 0, "model registry not initialized", nil)
	}
	if !provider.Supports(resolvedModel) {
		return nil, core.NewInvalidRequestError("unsupported model: "+resolvedModel, nil)
	}

	return &core.RequestModelResolution{
		RequestedModel:    model,
		RequestedProvider: providerHint,
		ResolvedSelector:  resolvedSelector,
		ProviderType:      strings.TrimSpace(provider.GetProviderType(resolvedModel)),
		AliasApplied:      aliasApplied,
	}, nil
}

func storeRequestModelResolution(c *echo.Context, resolution *core.RequestModelResolution) {
	if c == nil || resolution == nil {
		return
	}

	ctx := c.Request().Context()
	if plan := core.GetExecutionPlan(ctx); plan != nil {
		cloned := *plan
		cloned.ProviderType = resolution.ProviderType
		cloned.Resolution = resolution
		auditlog.EnrichEntryWithExecutionPlan(c, &cloned)
		ctx = core.WithExecutionPlan(ctx, &cloned)
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
	resolution, err := resolveAndStoreRequestModelResolution(c, provider, resolver, model, providerHint)
	return resolution, true, err
}

func currentRequestModelResolution(c *echo.Context) *core.RequestModelResolution {
	if c == nil {
		return nil
	}
	if plan := core.GetExecutionPlan(c.Request().Context()); plan != nil {
		return plan.Resolution
	}
	return nil
}

func resolveAndStoreRequestModelResolution(
	c *echo.Context,
	provider core.RoutableProvider,
	resolver RequestModelResolver,
	model, providerHint string,
) (*core.RequestModelResolution, error) {
	enrichAuditEntryWithRequestedModel(c, model, providerHint)

	resolution, err := resolveRequestModel(provider, resolver, model, providerHint)
	if err != nil {
		return nil, err
	}
	storeRequestModelResolution(c, resolution)
	return resolution, nil
}

func enrichAuditEntryWithRequestedModel(c *echo.Context, model, providerHint string) {
	if c == nil {
		return
	}
	model = strings.TrimSpace(model)
	providerHint = strings.TrimSpace(providerHint)
	if model == "" {
		return
	}
	plan := &core.ExecutionPlan{}
	if existing := core.GetExecutionPlan(c.Request().Context()); existing != nil {
		cloned := *existing
		plan = &cloned
	}
	plan.Resolution = &core.RequestModelResolution{
		RequestedModel:    model,
		RequestedProvider: providerHint,
	}
	auditlog.EnrichEntryWithExecutionPlan(c, plan)
}
