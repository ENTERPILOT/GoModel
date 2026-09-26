package server

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/goccy/go-json"
	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/gateway"
	"github.com/enterpilot/gomodel/internal/plugins"
)

// systemOneRoute is one System One route the gateway serves natively.
type systemOneRoute struct {
	// path is the gateway route; endpoint is the same route as a provider's
	// passthrough spells it, without the /v1 prefix.
	path     string
	endpoint string
	// operation names the call in provider metrics and logs.
	operation string
	// kevOnly marks the diagnostic routes only Kev servers implement; the
	// hosted API and OpenRouter serve the evaluation route alone.
	kevOnly bool
}

var (
	systemOneEvaluate = systemOneRoute{path: "/v1/systemone", endpoint: "systemone", operation: "systemone"}
	systemOnePermute  = systemOneRoute{path: "/v1/systemone/permute", endpoint: "systemone/permute", operation: "systemone_permute", kevOnly: true}
	systemOneSeparate = systemOneRoute{path: "/v1/systemone/separate", endpoint: "systemone/separate", operation: "systemone_separate", kevOnly: true}
)

const jevProviderType = "jev"

// systemOneProviderTypes are the provider types that serve the System One API
// natively: jev (TypeSafe's hosted Jev and self-hosted Kev servers) and
// OpenRouter, which serves Jev and Kev at the same path with the same request
// and answer shapes. Configuring either makes /v1/systemone available.
var systemOneProviderTypes = []string{jevProviderType, "openrouter"}

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
	return h.translatedInference().serveSystemOne(c, systemOneEvaluate)
}

// SystemOnePermute handles POST /v1/systemone/permute.
//
// @Summary      Run one Choice question with several option orders (Kev)
// @Description  A Kev server diagnostic: the request is a System One request, and n_perm (1 to 64, default 6) sets how many option orders run. Only jev providers pointing at a Kev server serve it.
// @Tags         systemone
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      object  true  "System One request with one Choice question"
// @Success      200      {object}  object  "Kev's answer, in the provider's shape"
// @Failure      400      {object}  core.OpenAIErrorEnvelope
// @Failure      401      {object}  core.OpenAIErrorEnvelope
// @Failure      404      {object}  core.OpenAIErrorEnvelope
// @Failure      429      {object}  core.OpenAIErrorEnvelope
// @Failure      502      {object}  core.OpenAIErrorEnvelope
// @Router       /v1/systemone/permute [post]
func (h *Handler) SystemOnePermute(c *echo.Context) error {
	return h.translatedInference().serveSystemOne(c, systemOnePermute)
}

// SystemOneSeparate handles POST /v1/systemone/separate.
//
// @Summary      Run each System One question in its own forward pass (Kev)
// @Description  A Kev server diagnostic that answers each question separately. Only jev providers pointing at a Kev server serve it.
// @Tags         systemone
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      object  true  "System One request: model, state, and questions"
// @Success      200      {object}  object  "Kev's answer, in the provider's shape"
// @Failure      400      {object}  core.OpenAIErrorEnvelope
// @Failure      401      {object}  core.OpenAIErrorEnvelope
// @Failure      404      {object}  core.OpenAIErrorEnvelope
// @Failure      429      {object}  core.OpenAIErrorEnvelope
// @Failure      502      {object}  core.OpenAIErrorEnvelope
// @Router       /v1/systemone/separate [post]
func (h *Handler) SystemOneSeparate(c *echo.Context) error {
	return h.translatedInference().serveSystemOne(c, systemOneSeparate)
}

// serveSystemOne resolves, guards, and forwards one System One request.
func (s *translatedInferenceService) serveSystemOne(c *echo.Context, route systemOneRoute) error {
	if !systemOneAvailable(s.provider) {
		return handleError(c, core.NewNotFoundError("POST "+route.path+" is available only when a jev or openrouter provider is configured"))
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
	resolution := workflow.Resolution
	if s.modelAuthorizer != nil {
		if err := s.modelAuthorizer.ValidateModelAccess(c.Request().Context(), resolution.ResolvedSelector); err != nil {
			return handleError(c, err)
		}
	}
	if reason := s.systemOneUnsupportedReason(route, resolution.ResolvedSelector, resolution.ProviderType); reason != "" {
		return handleError(c, systemOneUnsupportedModelError(c, route, resolution, reason))
	}

	body, err = s.guardSystemOneState(c, workflow, &req, body)
	if err != nil {
		return handleError(c, err)
	}
	return s.dispatchSystemOneWithCache(c, route, workflow, body)
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

// systemOneWorkflow resolves the request's model (virtual models first, then
// the catalog) and builds its workflow. A model the catalog does not list may
// still be a pinned version a jev provider accepts; see unlistedJevResolution.
func (s *translatedInferenceService) systemOneWorkflow(c *echo.Context, model string) (*core.Workflow, error) {
	if workflow := core.GetWorkflow(c.Request().Context()); workflow != nil && workflow.Resolution != nil {
		return workflow, nil
	}
	requested := core.NewRequestedModelSelector(model, "")
	resolution, ok := s.unlistedJevResolution(c.Request().Context(), requested)
	if ok {
		enrichAuditEntryWithRequestedModel(c, requested)
	} else {
		var err error
		resolution, err = resolveAndStoreRequestModelResolution(c, s.provider, s.modelResolver, nil, model, "")
		if err != nil {
			return nil, err
		}
	}
	workflow, err := translatedWorkflowForRequest(c, resolution, s.workflowPolicyResolver)
	if err != nil {
		return nil, err
	}
	storeWorkflow(c, workflow)
	return workflow, nil
}

// providerNamesByType lists configured provider instances of one type; the
// provider router implements it.
type providerNamesByType interface {
	ProviderNamesForType(providerType string) []string
}

// unlistedJevResolution routes a model the catalog does not list to a jev
// provider. TypeSafe lists only its aliases, yet accepts every versioned ID
// (jev-1.13.0) in the model field, so a pinned version must work without
// being declared first. The provider is the one the model names
// (jev/jev-1.13.0, kev/...), or for a bare name the only jev provider
// configured; virtual models apply first, so one can pin a version too. It
// is checked before the regular resolution, which would refresh the
// provider's model list on every such request; the upstream reports a name
// it rejects.
func (s *translatedInferenceService) unlistedJevResolution(ctx context.Context, requested core.RequestedModelSelector) (*core.RequestModelResolution, bool) {
	selector, aliasApplied, err := gateway.ResolveExecutionSelector(ctx, s.provider, s.modelResolver, requested)
	if err != nil || selector.Model == "" || s.provider.Supports(selector.QualifiedModel()) {
		return nil, false
	}
	providerName := ""
	switch named, _ := s.provider.(core.ProviderNameTypeResolver); {
	case selector.Provider == "":
		if lister, ok := s.provider.(providerNamesByType); ok {
			if names := lister.ProviderNamesForType(jevProviderType); len(names) == 1 {
				providerName = names[0]
			}
		}
	case named != nil && named.GetProviderTypeForName(selector.Provider) == jevProviderType:
		providerName = selector.Provider
	case selector.Provider == jevProviderType:
		if byType, ok := s.provider.(core.ProviderTypeNameResolver); ok {
			providerName = byType.GetProviderNameForType(jevProviderType)
		}
	}
	if strings.TrimSpace(providerName) == "" {
		return nil, false
	}
	return &core.RequestModelResolution{
		Requested:        requested,
		ResolvedSelector: core.ModelSelector{Provider: providerName, Model: selector.Model},
		ProviderType:     jevProviderType,
		ProviderName:     providerName,
		AliasApplied:     aliasApplied,
	}, true
}

// modelCatalog describes single catalog models; the provider router
// implements it.
type modelCatalog interface {
	LookupModel(model string) (*core.Model, bool)
}

// systemOneUnsupportedReason explains why a model cannot answer a request on
// route, or returns "" when it can. The provider must serve the API, a Kev
// diagnostic route needs a jev provider, and since OpenRouter also serves
// chat models, the model must not be catalogued with a generation mode. A
// model the catalog does not describe is given the benefit of the doubt: the
// upstream reports it if it is wrong.
func (s *translatedInferenceService) systemOneUnsupportedReason(route systemOneRoute, selector core.ModelSelector, providerType string) string {
	providerType = strings.TrimSpace(providerType)
	if !slices.Contains(systemOneProviderTypes, providerType) {
		return fmt.Sprintf("is served by provider type %s, which has no System One API", providerType)
	}
	if route.kevOnly && providerType != jevProviderType {
		return fmt.Sprintf("is served by provider type %s, which answers only %s; this route is served by Kev servers", providerType, systemOneEvaluate.path)
	}
	catalog, ok := s.provider.(modelCatalog)
	if !ok {
		return ""
	}
	model, ok := catalog.LookupModel(selector.QualifiedModel())
	if !ok || model == nil || model.Metadata == nil || len(model.Metadata.Modes) == 0 {
		return ""
	}
	return fmt.Sprintf("is a %s model, not a System One model", strings.Join(model.Metadata.Modes, "/"))
}

// systemOneUnsupportedModelError explains a request whose model cannot answer
// System One. It is also logged: a virtual model that sends System One
// traffic to a chat model is an operator mistake the caller cannot fix.
func systemOneUnsupportedModelError(c *echo.Context, route systemOneRoute, resolution *core.RequestModelResolution, reason string) error {
	requested := resolution.RequestedQualifiedModel()
	resolved := resolution.ResolvedQualifiedModel()
	slog.Warn("System One request routed to a model without the System One API",
		"request_id", requestIDFromContextOrHeader(c.Request()),
		"path", route.path,
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
		target, reason, route.path,
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
