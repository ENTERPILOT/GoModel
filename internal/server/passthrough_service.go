package server

import (
	"context"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/usage"
)

type passthroughService struct {
	provider                     core.RoutableProvider
	modelAuthorizer              RequestModelAuthorizer
	logger                       auditlog.LoggerInterface
	usageLogger                  usage.LoggerInterface
	budgetChecker                BudgetChecker
	rateLimiter                  RateLimiter
	pricingResolver              usage.PricingResolver
	normalizePassthroughV1Prefix bool
	enabledPassthroughProviders  map[string]struct{}
}

// validatePassthroughAccess checks a body that names a model like any model
// call. A request that names none, such as GET /files, acts on the whole
// provider account, so the caller must be granted the whole provider.
func (s *passthroughService) validatePassthroughAccess(ctx context.Context, info *core.PassthroughRouteInfo) error {
	if s.modelAuthorizer == nil {
		return nil
	}
	if selector, ok := passthroughAccessSelector(s.provider, info); ok {
		return s.modelAuthorizer.ValidateModelAccess(ctx, selector)
	}
	return s.modelAuthorizer.ValidateProviderAccess(ctx, info.ProviderName)
}

func (s *passthroughService) ProviderPassthrough(c *echo.Context) error {
	passthroughProvider, ok := s.provider.(core.RoutablePassthrough)
	if !ok {
		return handleError(c, core.NewInvalidRequestError("provider passthrough is not supported by the current provider router", nil))
	}

	providerType, providerName, endpoint, info, err := passthroughExecutionTarget(c, s.provider, s.normalizePassthroughV1Prefix)
	if err != nil {
		return handleError(c, err)
	}
	if !isEnabledPassthroughProvider(providerType, s.enabledPassthroughProviders) {
		return handleError(c, s.unsupportedPassthroughProviderError(providerType))
	}
	// The body is forwarded byte-for-byte, so it must not carry a repeated
	// selector field that the upstream parser could resolve differently from
	// the selector authorized below.
	if err := duplicateSelectorError(c); err != nil {
		return handleError(c, err)
	}
	if err := s.validatePassthroughAccess(c.Request().Context(), info); err != nil {
		return handleError(c, err)
	}
	adm, err := enforceAdmission(c, s.rateLimiter, s.budgetChecker, rateLimitRoute{provider: info.ProviderName, model: info.Model})
	if err != nil {
		return handleError(c, err)
	}
	defer adm.release()

	ctx, _ := requestContextWithRequestID(c.Request())
	c.SetRequest(c.Request().WithContext(ctx))
	resp, err := passthroughProvider.Passthrough(ctx, providerType, &core.PassthroughRequest{
		Method:          c.Request().Method,
		Endpoint:        endpoint,
		Operation:       info.GenAIOperation,
		Model:           info.Model,
		Stream:          info.Stream,
		StreamUncertain: info.StreamUncertain,
		Body:            c.Request().Body,
		Headers:         buildPassthroughHeaders(ctx, c.Request().Header),
		ProviderName:    providerName,
	})
	if err != nil {
		return handleError(c, err)
	}

	workflow := core.GetWorkflow(c.Request().Context())
	if workflow != nil {
		auditlog.EnrichEntryWithWorkflow(c, workflow)
	} else {
		auditlog.EnrichEntry(c, info.Model, providerType)
	}
	return s.proxyPassthroughResponse(c, providerType, providerName, endpoint, info, resp)
}
