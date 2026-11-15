package server

import (
	"context"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/gateway"
)

// RequestModelResolver resolves raw request selectors into concrete model
// selectors before provider execution.
type RequestModelResolver = gateway.ModelResolver

// RequestFailoverResolver resolves alternate concrete model selectors for a
// translated request after the primary selector has already been resolved.
type RequestFailoverResolver = gateway.FailoverResolver

func workflowProviderNameForType(provider core.RoutableProvider, providerType string) string {
	return gateway.WorkflowProviderNameForType(provider, providerType)
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

// resolveServiceModel maps a requested model to the concrete provider selector:
// virtual-model (alias) resolution first, then the provider registry. It is the
// same two-step gateway.ResolveExecutionSelector performs for chat, responses,
// and embeddings.
//
// The audio and realtime endpoints do not run the workflow middleware — their
// operations are absent from resolveWorkflow — so ensureRequestModelResolution
// never runs for them and they must resolve here instead. Resolving against the
// provider alone (the Router) only canonicalizes concrete registry ids and
// leaves an alias untouched, which then fails the downstream provider lookup.
//
// ctx must carry the effective request user path so user_path-scoped redirects
// apply. A nil resolver degrades to provider-only resolution.
func resolveServiceModel(
	ctx context.Context,
	provider core.RoutableProvider,
	resolver RequestModelResolver,
	model, providerHint string,
) (core.ModelSelector, error) {
	// Parse first so a malformed selector is a 400 rather than whatever the
	// resolver chain reports for it.
	if _, err := core.ParseModelSelector(model, providerHint); err != nil {
		return core.ModelSelector{}, core.NewInvalidRequestError(err.Error(), err)
	}
	selector, _, err := gateway.ResolveExecutionSelector(ctx, provider, resolver, core.NewRequestedModelSelector(model, providerHint))
	if err != nil {
		return core.ModelSelector{}, err
	}
	return selector, nil
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
		ctx = core.WithWorkflow(ctx, &cloned)
		c.SetRequest(c.Request().WithContext(ctx))
		auditlog.EnrichEntryWithWorkflow(c, &cloned)
	}
	if env := core.GetWhiteBoxPrompt(ctx); env != nil {
		env.RouteHints.Model = resolution.ResolvedSelector.Model
		env.RouteHints.Provider = resolution.ResolvedSelector.Provider
	}
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

	ctx := c.Request().Context()
	if routesImageInput(resolver) {
		ctx = core.WithImageInputProbe(ctx, func() bool { return requestHasImageInput(c) })
	}
	resolution, err := resolveRequestModelWithAuthorizer(ctx, provider, resolver, authorizer, requested)
	if err != nil {
		return nil, err
	}
	storeRequestModelResolution(c, resolution)
	return resolution, nil
}

// imageInputRouter is implemented by resolvers that route requests by
// whether they carry images (virtual models with vision routing).
type imageInputRouter interface {
	RoutesImageInput() bool
}

// routesImageInput reports whether resolver may route on image input. Other
// resolvers, and a virtual model service without vision routing, keep the
// probe (and its allocations) off the request path.
func routesImageInput(resolver RequestModelResolver) bool {
	router, ok := resolver.(imageInputRouter)
	return ok && router.RoutesImageInput()
}

// requestHasImageInput reports whether the request body carries an image
// part. Only a virtual model with vision routing asks, so other requests
// never read the body for it.
func requestHasImageInput(c *echo.Context) bool {
	body, err := requestBodyBytes(c)
	return err == nil && core.BodyHasImageInput(body)
}

func enrichAuditEntryWithRequestedModel(c *echo.Context, requested core.RequestedModelSelector) {
	if c == nil {
		return
	}
	requested = core.NewRequestedModelSelector(requested.Model, requested.ProviderHint)
	if requested.Model == "" {
		return
	}
	if existing := core.GetWorkflow(c.Request().Context()); existing != nil {
		cloned := *existing
		cloned.Resolution = &core.RequestModelResolution{
			Requested: requested,
		}
		auditlog.EnrichEntryWithWorkflow(c, &cloned)
		return
	}
	auditlog.EnrichEntryWithRequestedModel(c, requested.RequestedQualifiedModel())
}
