package server

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/goccy/go-json"
	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/gateway"
	"github.com/enterpilot/gomodel/internal/plugins"
)

const (
	systemOnePath     = "/v1/systemone"
	systemOneEndpoint = "systemone"
	// jevProviderType serves TypeSafe's hosted Jev API and self-hosted Kev
	// servers; configuring one is what makes /v1/systemone available.
	jevProviderType = "jev"
)

// systemOneProviderTypes are the provider types that serve the System One API
// natively. OpenRouter serves Jev at the same path with the same request and
// answer shapes, so it can back a virtual model next to a jev provider.
var systemOneProviderTypes = map[string]struct{}{
	jevProviderType: {},
	"openrouter":    {},
}

// SystemOne handles POST /v1/systemone.
//
// It accepts TypeSafe's System One decision request (a state plus a map of
// typed questions) and forwards it natively: the body reaches the provider
// unchanged apart from the routed model name and guardrail edits to the
// state. Requests are never translated to another API, so a model whose
// provider has no System One API is rejected.
//
// @Summary      Evaluate a System One decision request (Jev / Kev)
// @Description  Available when a jev provider is configured. The request and answer follow TypeSafe's System One API; models on providers without that API are rejected rather than translated.
// @Tags         systemone
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      object  true  "System One request: model, state, and questions"
// @Success      200      {object}  object  "System One answers, in the provider's shape"
// @Failure      400      {object}  core.OpenAIErrorEnvelope
// @Failure      401      {object}  core.OpenAIErrorEnvelope
// @Failure      404      {object}  core.OpenAIErrorEnvelope
// @Failure      429      {object}  core.OpenAIErrorEnvelope
// @Failure      502      {object}  core.OpenAIErrorEnvelope
// @Router       /v1/systemone [post]
func (h *Handler) SystemOne(c *echo.Context) error {
	return h.translatedInference().SystemOne(c)
}

// SystemOne resolves, guards, and forwards one System One request.
func (s *translatedInferenceService) SystemOne(c *echo.Context) error {
	if !s.systemOneAvailable() {
		return handleError(c, core.NewNotFoundError("POST "+systemOnePath+" is available only when a jev provider is configured"))
	}
	body, err := requestBodyBytes(c)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	var req core.SystemOneRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	if strings.TrimSpace(req.Model) == "" {
		return handleError(c, core.NewInvalidRequestError("model is required", nil).WithParam("model"))
	}

	workflow, err := s.systemOneWorkflow(c, req.Model)
	if err != nil {
		return handleError(c, err)
	}
	ctx := c.Request().Context()
	resolution := workflow.Resolution
	if s.modelAuthorizer != nil {
		if err := s.modelAuthorizer.ValidateModelAccess(ctx, resolution.ResolvedSelector); err != nil {
			return handleError(c, err)
		}
	}
	providerType := strings.TrimSpace(resolution.ProviderType)
	if _, ok := systemOneProviderTypes[providerType]; !ok {
		return handleError(c, systemOneUnsupportedModelError(c, resolution))
	}

	body, err = s.guardSystemOneState(c, workflow, &req, body)
	if err != nil {
		return handleError(c, err)
	}
	model := resolution.ResolvedSelector.Model
	if body, err = rewriteMessagesModel(body, model); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	return s.dispatchSystemOne(c, workflow, model, body)
}

// systemOneAvailable reports whether a jev provider is configured. It is
// checked per request rather than at route registration so a provider added
// at runtime makes the endpoint available without a restart.
func (s *translatedInferenceService) systemOneAvailable() bool {
	named, ok := s.provider.(core.ProviderTypeNameResolver)
	return ok && strings.TrimSpace(named.GetProviderNameForType(jevProviderType)) != ""
}

// systemOneWorkflow returns the request's workflow with its model resolved.
// The workflow middleware resolves it from the body; a request that reached
// the handler without one (the body was not parsed there) is resolved here,
// so virtual models and workflow policy apply either way.
func (s *translatedInferenceService) systemOneWorkflow(c *echo.Context, model string) (*core.Workflow, error) {
	if workflow := core.GetWorkflow(c.Request().Context()); workflow != nil && workflow.Resolution != nil {
		return workflow, nil
	}
	resolution, err := resolveAndStoreRequestModelResolution(c, s.provider, s.modelResolver, nil, model, "")
	if err != nil {
		return nil, err
	}
	workflow, err := translatedWorkflowForRequest(c, resolution, s.workflowPolicyResolver)
	if err != nil {
		return nil, err
	}
	storeWorkflow(c, workflow)
	return workflow, nil
}

// systemOneUnsupportedModelError explains a request whose model routes to a
// provider without the System One API. It is also logged: a virtual model
// that sends System One traffic to a chat model is an operator mistake the
// caller cannot fix.
func systemOneUnsupportedModelError(c *echo.Context, resolution *core.RequestModelResolution) error {
	requested := resolution.RequestedQualifiedModel()
	resolved := resolution.ResolvedQualifiedModel()
	slog.Warn("System One request routed to a provider without the System One API",
		"request_id", requestIDFromContextOrHeader(c.Request()),
		"requested_model", requested,
		"resolved_model", resolved,
		"provider_type", resolution.ProviderType,
	)
	target := fmt.Sprintf("%q", requested)
	if resolved != requested {
		target += fmt.Sprintf(" (resolved to %q)", resolved)
	}
	return core.NewInvalidRequestError(fmt.Sprintf(
		"model %s is served by a %s provider, which has no System One API; %s forwards requests natively and does not translate them to other APIs, so use a jev model",
		target, resolution.ProviderType, systemOnePath,
	), nil).WithParam("model")
}

// guardSystemOneState runs the workflow's prompt guardrails over the state
// and returns the body carrying their edits. A guardrail that answers the
// request itself blocks it instead: its answer is chat text, and a System
// One caller expects typed answers.
func (s *translatedInferenceService) guardSystemOneState(c *echo.Context, workflow *core.Workflow, req *core.SystemOneRequest, body []byte) ([]byte, error) {
	patcher, ok := s.translatedRequestPatcher.(gateway.SystemOneRequestPatcher)
	if !ok || !workflow.GuardrailsEnabled() {
		return body, nil
	}
	patched, err := patcher.PatchSystemOneRequest(c.Request().Context(), req)
	s.recordGuardrailOutcomes(c)
	if err != nil {
		if short := shortCircuitOf(err); short != nil {
			return nil, plugins.BlockError(short.Decision, http.StatusBadRequest)
		}
		return nil, err
	}
	if patched == nil || patched == req || bytes.Equal(patched.State, req.State) {
		return body, nil
	}
	rewritten, err := replaceTopLevelMember(body, "state", patched.State)
	if err != nil {
		return nil, core.NewInvalidRequestError("invalid request body: "+err.Error(), err)
	}
	return rewritten, nil
}

// dispatchSystemOne forwards the body to the resolved provider and relays its
// answer unchanged, with admission, audit, and usage accounting.
func (s *translatedInferenceService) dispatchSystemOne(c *echo.Context, workflow *core.Workflow, model string, body []byte) error {
	passthroughProvider, ok := s.provider.(core.RoutablePassthrough)
	if !ok {
		return handleError(c, core.NewInvalidRequestError("provider passthrough is not supported by the current provider router", nil))
	}
	s.observeLiveProviderAttempts(c, workflow)

	adm, err := enforceAdmission(c, s.rateLimiter, s.budgetChecker, rateLimitRouteFromWorkflow(workflow))
	if err != nil {
		return handleError(c, err)
	}
	defer adm.release()
	ctx := adm.dispatchContext(c.Request().Context())

	resolution := workflow.Resolution
	providerType := strings.TrimSpace(resolution.ProviderType)
	providerName := strings.TrimSpace(resolution.ProviderName)
	resp, err := passthroughProvider.Passthrough(ctx, providerType, &core.PassthroughRequest{
		Method:       http.MethodPost,
		Endpoint:     systemOneEndpoint,
		Operation:    "systemone",
		Model:        model,
		Body:         io.NopCloser(bytes.NewReader(body)),
		Headers:      buildPassthroughHeaders(ctx, c.Request().Header),
		ProviderName: providerName,
	})
	if err != nil {
		return handleError(c, err)
	}

	auditlog.EnrichEntryWithWorkflow(c, workflow)
	auditlog.EnrichEntryWithResolvedRoute(c, resolution.ResolvedQualifiedModel(), providerType, providerName)
	info := &core.PassthroughRouteInfo{
		Provider:           providerType,
		ProviderName:       providerName,
		NormalizedEndpoint: systemOneEndpoint,
		SemanticOperation:  "systemone",
		AuditPath:          systemOnePath,
		Model:              model,
	}
	return proxyPassthroughResponse(c, s.logger, s.usageLogger, s.pricingResolver, providerType, providerName, systemOneEndpoint, info, resp)
}
