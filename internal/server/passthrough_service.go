package server

import (
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
	if s.modelAuthorizer != nil {
		if selector, ok := passthroughAccessSelector(s.provider, info); ok {
			if err := s.modelAuthorizer.ValidateModelAccess(c.Request().Context(), selector); err != nil {
				return handleError(c, err)
			}
		}
	}
	adm, err := enforceAdmission(c, s.rateLimiter, s.budgetChecker, rateLimitRoute{provider: info.ProviderName, model: info.Model})
	if err != nil {
		return handleError(c, err)
	}
	defer adm.release()

	ctx, _ := requestContextWithRequestID(c.Request())
	c.SetRequest(c.Request().WithContext(ctx))
	resp, err := passthroughProvider.Passthrough(ctx, providerType, &core.PassthroughRequest{
		Method:       c.Request().Method,
		Endpoint:     endpoint,
		Body:         c.Request().Body,
		Headers:      buildPassthroughHeaders(ctx, c.Request().Header),
		ProviderName: providerName,
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
	return s.proxyPassthroughResponse(c, providerType, providerNameFromWorkflow(workflow), endpoint, info, resp)
}
