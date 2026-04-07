package server

import (
	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
	"gomodel/internal/usage"
)

type passthroughService struct {
	provider                     core.RoutableProvider
	logger                       auditlog.LoggerInterface
	usageLogger                  usage.LoggerInterface
	pricingResolver              usage.PricingResolver
	normalizePassthroughV1Prefix bool
	enabledPassthroughProviders  map[string]struct{}
}

func (s *passthroughService) ProviderPassthrough(c *echo.Context) error {
	passthroughProvider, ok := s.provider.(core.RoutablePassthrough)
	if !ok {
		return handleError(c, core.NewInvalidRequestError("provider passthrough is not supported by the current provider router", nil))
	}

	providerType, endpoint, info, err := passthroughExecutionTarget(c, s.normalizePassthroughV1Prefix)
	if err != nil {
		return handleError(c, err)
	}
	if !isEnabledPassthroughProvider(providerType, s.enabledPassthroughProviders) {
		return handleError(c, s.unsupportedPassthroughProviderError(providerType))
	}

	ctx, _ := requestContextWithRequestID(c.Request())
	c.SetRequest(c.Request().WithContext(ctx))
	resp, err := passthroughProvider.Passthrough(ctx, providerType, &core.PassthroughRequest{
		Method:   c.Request().Method,
		Endpoint: endpoint,
		Body:     c.Request().Body,
		Headers:  buildPassthroughHeaders(ctx, c.Request().Header),
	})
	if err != nil {
		return handleError(c, err)
	}

	plan := core.GetExecutionPlan(c.Request().Context())
	if plan != nil {
		auditlog.EnrichEntryWithExecutionPlan(c, plan)
	} else {
		auditlog.EnrichEntry(c, info.Model, providerType)
	}
	return s.proxyPassthroughResponse(c, providerType, providerNameFromPlan(plan), endpoint, info, resp)
}
