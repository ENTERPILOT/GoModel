package server

import (
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
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

// guardrailPresenceResolver reports whether the gateway configures any
// guardrail chain at all. Implemented by the workflows service.
type guardrailPresenceResolver interface {
	HasGuardrailChains() bool
}

// guardrailWorkflowApplies reports whether a guardrail chain applies, or may
// apply, to this passthrough request.
//
// The workflow of a passthrough request is matched on the model read from its
// provider-native body, and that read is best effort: a body the gateway
// cannot parse (an unknown dialect, a chunked or oversized payload) leaves the
// model empty, and a model-scoped guardrail workflow is then never matched.
// So when the model is unknown on an inference call, any guardrail anywhere in
// the configuration counts as applying — the caller controls the body, and a
// policy must not be escapable by making it unreadable.
func (s *passthroughService) guardrailWorkflowApplies(c *echo.Context, info *core.PassthroughRouteInfo) bool {
	if s.allowUnguardedPassthrough || s.pluginChains == nil || info == nil {
		return false
	}
	// Guardrails only ever run on text inference, so only those routes can
	// lose a policy by being called through passthrough. Listing models or
	// managing files is unaffected and keeps working.
	if !passthroughRunsTextInference(info) {
		return false
	}
	chains := s.pluginChains.ChainsForContext(c.Request().Context())
	if chains != nil && (!chains.Prompt.Empty() || !chains.Response.Empty() || !chains.Stream.Empty()) {
		return true
	}
	if strings.TrimSpace(info.Model) != "" {
		return false
	}
	presence, ok := s.pluginChains.(guardrailPresenceResolver)
	return ok && presence.HasGuardrailChains()
}

// genAIOperationChat and genAIOperationTextCompletion are the GenAI operations
// passthrough route semantics give text inference endpoints: chat completions,
// responses and Anthropic messages for the first, the legacy completion
// endpoints for the second.
const (
	genAIOperationChat           = "chat"
	genAIOperationTextCompletion = "text_completion"
)

// textInferenceEndpointSuffixes are the provider-native endpoint paths that run
// text inference, matched on the tail of the endpoint so a dialect's version
// segment (`/v2/chat`, `/beta/completions`) still counts.
var textInferenceEndpointSuffixes = []string{
	"/chat/completions",
	"/chat",
	"/completions",
	"/responses",
	"/messages",
	"/converse",
	"/converse-stream",
	":generatecontent",
	":streamgeneratecontent",
}

// passthroughRunsTextInference reports whether the passthrough route generates
// text, which is the only kind of route a guardrail chain would have run on.
//
// The route's GenAI operation decides it, but only a provider that ships a
// passthrough semantics table names one, and only for the endpoints the table
// lists. A provider without a table leaves every operation empty, so the
// endpoint path decides those: the refusal must not be escapable by sending the
// same body to a provider GoModel has no semantics for.
func passthroughRunsTextInference(info *core.PassthroughRouteInfo) bool {
	switch strings.TrimSpace(info.GenAIOperation) {
	case genAIOperationChat, genAIOperationTextCompletion:
		return true
	case "":
		return textInferenceEndpoint(providers.PassthroughEndpointPath(info))
	default:
		// A named non-text operation (embeddings, images, audio) runs no
		// guardrail chain on /v1 either.
		return false
	}
}

func textInferenceEndpoint(path string) bool {
	path = strings.ToLower(strings.TrimRight(path, "/"))
	if path == "" {
		return false
	}
	for _, suffix := range textInferenceEndpointSuffixes {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return false
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
	if s.guardrailWorkflowApplies(c, info) {
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
