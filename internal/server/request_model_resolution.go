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
	ResolveModel(requested core.RequestedModelSelector) (core.ModelSelector, bool, error)
}

// RequestFallbackResolver resolves alternate concrete model selectors for a
// translated request after the primary selector has already been resolved.
type RequestFallbackResolver interface {
	ResolveFallbacks(resolution *core.RequestModelResolution, op core.Operation) []core.ModelSelector
}

func resolvedProviderName(provider core.RoutableProvider, selector core.ModelSelector, fallback string) string {
	fallback = strings.TrimSpace(fallback)
	if provider == nil {
		return fallback
	}
	if named, ok := provider.(core.ProviderNameResolver); ok {
		if providerName := strings.TrimSpace(named.GetProviderName(selector.QualifiedModel())); providerName != "" {
			return providerName
		}
	}
	return fallback
}

func resolvedWorkflowProviderName(resolution *core.RequestModelResolution) string {
	if resolution == nil {
		return ""
	}
	if providerName := strings.TrimSpace(resolution.ProviderName); providerName != "" {
		return providerName
	}
	return strings.TrimSpace(resolution.ResolvedSelector.Provider)
}

func workflowProviderNameForType(provider core.RoutableProvider, providerType string) string {
	providerType = strings.TrimSpace(providerType)
	if providerType == "" || provider == nil {
		return ""
	}
	if named, ok := provider.(core.ProviderTypeNameResolver); ok {
		return strings.TrimSpace(named.GetProviderNameForType(providerType))
	}
	return ""
}

func resolveRequestModel(provider core.RoutableProvider, resolver RequestModelResolver, requested core.RequestedModelSelector) (*core.RequestModelResolution, error) {
	requested = core.NewRequestedModelSelector(requested.Model, requested.ProviderHint)

	resolvedSelector, aliasApplied, err := resolveExecutionSelector(provider, resolver, requested)
	if err != nil {
		return nil, core.NewInvalidRequestError(err.Error(), err)
	}
	if resolvedSelector == (core.ModelSelector{}) {
		resolvedSelector, err = requested.Normalize()
		if err != nil {
			return nil, core.NewInvalidRequestError(err.Error(), err)
		}
	}

	resolvedModel := resolvedSelector.QualifiedModel()
	if counted, ok := provider.(modelCountProvider); ok && counted.ModelCount() == 0 {
		return nil, core.NewProviderError("", 0, "model registry not initialized", nil)
	}
	if !provider.Supports(resolvedModel) {
		return nil, core.NewInvalidRequestError("unsupported model: "+resolvedModel, nil)
	}

	return &core.RequestModelResolution{
		Requested:        requested,
		ResolvedSelector: resolvedSelector,
		ProviderType:     strings.TrimSpace(provider.GetProviderType(resolvedModel)),
		ProviderName:     resolvedProviderName(provider, resolvedSelector, ""),
		AliasApplied:     aliasApplied,
	}, nil
}

func resolveExecutionSelector(
	provider core.RoutableProvider,
	resolver RequestModelResolver,
	requested core.RequestedModelSelector,
) (core.ModelSelector, bool, error) {
	requested = core.NewRequestedModelSelector(requested.Model, requested.ProviderHint)

	var (
		resolvedSelector core.ModelSelector
		aliasApplied     bool
		err              error
	)

	if resolver != nil {
		resolvedSelector, aliasApplied, err = resolver.ResolveModel(requested)
		if err != nil {
			return core.ModelSelector{}, false, err
		}
		requested = core.NewRequestedModelSelector(resolvedSelector.QualifiedModel(), "")
	}

	if providerResolver, ok := provider.(RequestModelResolver); ok {
		var providerChanged bool
		resolvedSelector, providerChanged, err = providerResolver.ResolveModel(requested)
		if err != nil {
			return core.ModelSelector{}, false, err
		}
		return resolvedSelector, aliasApplied || providerChanged, nil
	}

	if resolvedSelector != (core.ModelSelector{}) {
		return resolvedSelector, aliasApplied, nil
	}

	resolvedSelector, err = requested.Normalize()
	return resolvedSelector, aliasApplied, err
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
	requested := core.NewRequestedModelSelector(model, providerHint)
	enrichAuditEntryWithRequestedModel(c, requested)

	resolution, err := resolveRequestModel(provider, resolver, requested)
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
	plan := &core.ExecutionPlan{}
	if existing := core.GetExecutionPlan(c.Request().Context()); existing != nil {
		cloned := *existing
		plan = &cloned
	}
	plan.Resolution = &core.RequestModelResolution{
		Requested: requested,
	}
	auditlog.EnrichEntryWithExecutionPlan(c, plan)
}
