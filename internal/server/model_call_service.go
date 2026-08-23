package server

import (
	"context"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/usage"
)

// modelCallService is the shared core of the non-ingress-managed model
// endpoints (audio, images): resolve and authorize the model, enforce budget,
// route to the provider, and record usage. Endpoint services embed it and add
// only their own transport handling.
type modelCallService struct {
	provider        core.RoutableProvider
	modelResolver   RequestModelResolver
	modelAuthorizer RequestModelAuthorizer
	budgetChecker   BudgetChecker
	rateLimiter     RateLimiter
	// usageLogger and pricingResolver record per-call usage. These providers
	// return opaque or non-token payloads, so each service derives its own
	// usage entry and hands it to logUsage.
	usageLogger     usage.LoggerInterface
	pricingResolver usage.PricingResolver
}

// modelCallRoute carries the resolved routing identity for a single call,
// used to label its usage entry the same way the inference orchestrator does.
// selector is the fully resolved model, kept so callers can rewrite the outgoing
// request to the concrete provider model rather than the requested alias.
type modelCallRoute struct {
	selector     core.ModelSelector
	model        string
	providerType string
	providerName string
	requestID    string
	slowdown     float64
}

// prepare resolves and authorizes the model, enforces budget, and stamps the
// request id, returning the context to dispatch with and the resolved route.
// Authorization runs on the fully resolved selector so model-override and
// user-path rules see the same concrete provider name as the inference orchestrator.
func (s *modelCallService) prepare(c *echo.Context, model, providerHint string) (context.Context, modelCallRoute, error) {
	// Surface resolution failures (unknown alias, registry not ready, malformed
	// selector) instead of authorizing an unresolved selector.
	selector, err := resolveServiceModel(c.Request().Context(), s.provider, s.modelResolver, model, providerHint)
	if err != nil {
		return nil, modelCallRoute{}, err
	}
	if s.modelAuthorizer != nil {
		if err := s.modelAuthorizer.ValidateModelAccess(c.Request().Context(), selector); err != nil {
			return nil, modelCallRoute{}, err
		}
	}
	if err := enforceBudget(c, s.budgetChecker); err != nil {
		return nil, modelCallRoute{}, err
	}
	auditlog.EnrichEntry(c, selector.Model, "")

	ctx, requestID := requestContextWithRequestID(c.Request())
	c.SetRequest(c.Request().WithContext(ctx))
	route := s.routeFor(selector, requestID)
	// Stamp the executed route so audit rows carry the resolved model and
	// provider, as the inference orchestrator does for chat.
	auditlog.EnrichEntryWithResolvedRoute(c, selector.QualifiedModel(), route.providerType, route.providerName)
	route.slowdown = resolveModelSlowdown(
		ctx,
		s.modelResolver,
		core.NewRequestedModelSelector(model, providerHint),
		selector,
	)
	return ctx, route, nil
}

// routeFor maps a resolved selector to its canonical provider type and concrete
// instance name. The name falls back to the selector's provider when the router
// exposes no name resolver.
func (s *modelCallService) routeFor(selector core.ModelSelector, requestID string) modelCallRoute {
	qualified := selector.QualifiedModel()
	route := modelCallRoute{
		selector:     selector,
		model:        selector.Model,
		providerType: s.provider.GetProviderType(qualified),
		providerName: selector.Provider,
		requestID:    requestID,
	}
	if resolver, ok := s.provider.(core.ProviderNameResolver); ok {
		if name := resolver.GetProviderName(qualified); name != "" {
			route.providerName = name
		}
	}
	return route
}

// logUsage records one usage entry for a routed call when usage tracking is on.
// It mirrors the inference orchestrator: resolve pricing, extract the entry, then
// stamp the concrete provider name and user path before the non-blocking write.
func (s *modelCallService) logUsage(ctx context.Context, route modelCallRoute, extract func(*core.ModelPricing) *usage.UsageEntry) {
	if s.usageLogger == nil || !s.usageLogger.Config().Enabled {
		return
	}
	var pricing *core.ModelPricing
	if s.pricingResolver != nil {
		pricingProvider := route.providerName
		if pricingProvider == "" {
			pricingProvider = route.providerType
		}
		pricing = s.pricingResolver.ResolvePricing(route.model, pricingProvider)
	}
	entry := extract(pricing)
	if entry == nil {
		return
	}
	entry.ProviderName = strings.TrimSpace(route.providerName)
	entry.UserPath = core.UserPathFromContext(ctx)
	entry.SessionID = core.SessionIDFromContext(ctx)
	entry.Labels = core.RequestLabelsFromContext(ctx)
	s.usageLogger.Write(entry)
}
