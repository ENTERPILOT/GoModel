package server

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
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
)

// systemOneProviderTypes are the provider types that serve the System One API
// natively: jev (TypeSafe's hosted Jev and self-hosted Kev servers) and
// OpenRouter, which serves Jev and Kev at the same path with the same request
// and answer shapes. Configuring either makes /v1/systemone available.
var systemOneProviderTypes = []string{"jev", "openrouter"}

// SystemOne handles POST /v1/systemone.
//
// It accepts TypeSafe's System One decision request (a state plus a map of
// typed questions) and forwards it natively: the body reaches the provider
// unchanged apart from the routed model name and guardrail edits to the
// state. Requests are never translated to another API, so a model whose
// provider has no System One API is rejected.
//
// @Summary      Evaluate a System One decision request (Jev / Kev)
// @Description  Available when a jev or openrouter provider is configured. The request and answer follow TypeSafe's System One API; models on providers without that API are rejected rather than translated.
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
	if !systemOneAvailable(s.provider) {
		return handleError(c, core.NewNotFoundError("POST "+systemOnePath+" is available only when a jev or openrouter provider is configured"))
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
	if reason := s.systemOneUnsupportedReason(resolution); reason != "" {
		return handleError(c, systemOneUnsupportedModelError(c, resolution, reason))
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

// systemOneAvailable reports whether a provider that serves System One is
// configured. It is checked per request rather than at route registration so
// a provider added at runtime makes the endpoint available without a restart.
func systemOneAvailable(provider core.RoutableProvider) bool {
	named, ok := provider.(core.ProviderTypeNameResolver)
	if !ok {
		return false
	}
	for _, providerType := range systemOneProviderTypes {
		if strings.TrimSpace(named.GetProviderNameForType(providerType)) != "" {
			return true
		}
	}
	return false
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

// modelCatalog describes single catalog models; the provider router
// implements it.
type modelCatalog interface {
	LookupModel(model string) (*core.Model, bool)
}

// systemOneUnsupportedReason explains why the resolved model cannot answer a
// System One request, or returns "" when it can. The provider must serve the
// API, and since OpenRouter also serves chat models, the model must not be
// catalogued with a generation mode. A model the catalog does not describe is
// given the benefit of the doubt: the upstream reports it if it is wrong.
func (s *translatedInferenceService) systemOneUnsupportedReason(resolution *core.RequestModelResolution) string {
	providerType := strings.TrimSpace(resolution.ProviderType)
	if !slices.Contains(systemOneProviderTypes, providerType) {
		return fmt.Sprintf("is served by a %s provider, which has no System One API", providerType)
	}
	catalog, ok := s.provider.(modelCatalog)
	if !ok {
		return ""
	}
	model, ok := catalog.LookupModel(resolution.ResolvedQualifiedModel())
	if !ok || model == nil || model.Metadata == nil || len(model.Metadata.Modes) == 0 {
		return ""
	}
	return fmt.Sprintf("is a %s model, not a System One model", strings.Join(model.Metadata.Modes, "/"))
}

// systemOneUnsupportedModelError explains a request whose model cannot answer
// System One. It is also logged: a virtual model that sends System One
// traffic to a chat model is an operator mistake the caller cannot fix.
func systemOneUnsupportedModelError(c *echo.Context, resolution *core.RequestModelResolution, reason string) error {
	requested := resolution.RequestedQualifiedModel()
	resolved := resolution.ResolvedQualifiedModel()
	slog.Warn("System One request routed to a model without the System One API",
		"request_id", requestIDFromContextOrHeader(c.Request()),
		"requested_model", requested,
		"resolved_model", resolved,
		"provider_type", resolution.ProviderType,
		"reason", reason,
	)
	target := fmt.Sprintf("%q", requested)
	if resolved != requested {
		target += fmt.Sprintf(" (resolved to %q)", resolved)
	}
	return core.NewInvalidRequestError(fmt.Sprintf(
		"model %s %s; %s forwards requests natively and does not translate them to other APIs, so use a System One model such as a jev model or OpenRouter's typesafe/jev-1.13",
		target, reason, systemOnePath,
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
	// Compare against a copy: a patcher may redact the state in place and
	// return the same request, and that edit must still reach the body.
	original := bytes.Clone(req.State)
	patched, err := patcher.PatchSystemOneRequest(c.Request().Context(), req)
	s.recordGuardrailOutcomes(c)
	if err != nil {
		if short := shortCircuitOf(err); short != nil {
			return nil, plugins.BlockError(short.Decision, http.StatusBadRequest)
		}
		return nil, err
	}
	if patched == nil || bytes.Equal(patched.State, original) {
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
