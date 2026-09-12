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
	pluginChains                 PluginChainsResolver
	allowUnguardedPassthrough    bool
}

// guardrailsBypassedError reports a passthrough request that a guardrail
// workflow applies to. Passthrough forwards the caller's provider-native body
// and relays the provider's answer untouched, so no guardrail chain can run
// over them; serving the request anyway would silently drop a policy the
// operator configured. Refusing is the fail-safe default; set
// server.allow_unguarded_passthrough (ALLOW_UNGUARDED_PASSTHROUGH=true) to
// accept the gap, or scope the workflow so it does not match these callers.
func guardrailsBypassedError(providerType string) error {
	return core.NewPermissionError(
		"provider passthrough cannot run guardrails, and a guardrail workflow applies to this request; " +
			"call the OpenAI-compatible endpoints (/v1/chat/completions, /v1/responses, /v1/messages) instead, " +
			"or set server.allow_unguarded_passthrough to allow unguarded passthrough for " + providerType,
	).WithCode("passthrough_guardrails_unsupported")
}

// guardrailWorkflowApplies reports whether the request's matched workflow runs
// any guardrail chain.
func (s *passthroughService) guardrailWorkflowApplies(c *echo.Context) bool {
	if s.allowUnguardedPassthrough || s.pluginChains == nil {
		return false
	}
	chains := s.pluginChains.ChainsForContext(c.Request().Context())
	return chains != nil && (!chains.Prompt.Empty() || !chains.Response.Empty() || !chains.Stream.Empty())
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
	if s.guardrailWorkflowApplies(c) {
		return handleError(c, guardrailsBypassedError(providerType))
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
