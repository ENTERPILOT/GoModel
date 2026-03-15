package server

import (
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
)

type resolvedModelProvider interface {
	ResolveModel(model, provider string) (core.ModelSelector, bool, error)
}

func resolveRequestModel(provider core.RoutableProvider, model, providerHint string) (*core.RequestModelResolution, error) {
	model = strings.TrimSpace(model)
	providerHint = strings.TrimSpace(providerHint)

	var (
		resolvedSelector core.ModelSelector
		aliasApplied     bool
		err              error
	)

	if resolver, ok := provider.(resolvedModelProvider); ok {
		resolvedSelector, aliasApplied, err = resolver.ResolveModel(model, providerHint)
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

	ctx := core.WithRequestModelResolution(c.Request().Context(), resolution)
	if env := core.GetWhiteBoxPrompt(ctx); env != nil {
		env.RouteHints.Model = resolution.ResolvedSelector.Model
		env.RouteHints.Provider = resolution.ResolvedSelector.Provider
	}
	c.SetRequest(c.Request().WithContext(ctx))
}

func ensureRequestModelResolution(c *echo.Context, provider core.RoutableProvider) (*core.RequestModelResolution, bool, error) {
	if c == nil {
		return nil, false, nil
	}
	if resolution := core.GetRequestModelResolution(c.Request().Context()); resolution != nil {
		return resolution, true, nil
	}

	model, providerHint, parsed, err := selectorHintsForValidation(c)
	if err != nil || !parsed {
		return nil, parsed, err
	}
	enrichAuditEntryWithRequestedModel(c, model, providerHint)

	resolution, err := resolveRequestModel(provider, model, providerHint)
	if err != nil {
		return nil, true, err
	}
	storeRequestModelResolution(c, resolution)
	return resolution, true, nil
}

func applyRequestModelResolution(c *echo.Context, provider core.RoutableProvider, model, providerHint *string) error {
	if model == nil || providerHint == nil {
		return core.NewInvalidRequestError("model selector targets are required", nil)
	}

	resolution := core.GetRequestModelResolution(c.Request().Context())
	if resolution == nil {
		enrichAuditEntryWithRequestedModel(c, *model, *providerHint)
		var err error
		resolution, err = resolveRequestModel(provider, *model, *providerHint)
		if err != nil {
			return err
		}
		storeRequestModelResolution(c, resolution)
	}

	*model = resolution.ResolvedSelector.Model
	*providerHint = resolution.ResolvedSelector.Provider
	return nil
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
	auditlog.EnrichEntryWithResolution(c, &core.RequestModelResolution{
		RequestedModel:    model,
		RequestedProvider: providerHint,
	})
}
