package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/goccy/go-json"
	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/gateway"
)

var decisionsRoute = nativeJSONRoute{path: "/v1/decisions", endpoint: "decisions", operation: "decisions"}

// Decisions handles POST /v1/decisions.
//
// It accepts OpenAI's Decisions request (an input plus a list of predicate,
// choice, and score questions) and forwards it to a provider that answers
// it: OpenAI natively, and jev (Jev and Kev) by translating to System One
// and back, so the answer is always OpenAI's shape.
//
// @Summary      Answer typed questions about an input (OpenAI Decisions)
// @Description  Available when an openai or jev provider is configured. OpenAI models answer natively; jev models (Jev, Kev) answer through System One, translated both ways, and read text input only.
// @Tags         decisions
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      object  true  "Decisions request: model, input, and questions"
// @Success      200      {object}  object  "Decision: answers in question order, model, and usage"
// @Failure      400      {object}  core.OpenAIErrorEnvelope
// @Failure      401      {object}  core.OpenAIErrorEnvelope
// @Failure      404      {object}  core.OpenAIErrorEnvelope
// @Failure      429      {object}  core.OpenAIErrorEnvelope
// @Failure      502      {object}  core.OpenAIErrorEnvelope
// @Router       /v1/decisions [post]
func (h *Handler) Decisions(c *echo.Context) error {
	return h.translatedInference().serveDecisions(c)
}

// serveDecisions resolves, guards, and forwards one Decisions request.
func (s *translatedInferenceService) serveDecisions(c *echo.Context) error {
	router, ok := s.provider.(core.RoutableDecisions)
	if !ok || len(router.DecisionsProviderTypes()) == 0 {
		return handleError(c, core.NewNotFoundError("POST "+decisionsRoute.path+" is available only when an openai or jev provider is configured"))
	}
	body, err := requestBodyBytes(c)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	var req core.DecisionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	if strings.TrimSpace(req.Model) == "" {
		return handleError(c, core.NewInvalidRequestError("model is required", nil).WithParam("model"))
	}

	workflow, err := s.decisionWorkflow(c, req.Model)
	if err != nil {
		return handleError(c, err)
	}
	resolution := workflow.Resolution
	if s.modelAuthorizer != nil {
		if err := s.modelAuthorizer.ValidateModelAccess(c.Request().Context(), resolution.ResolvedSelector); err != nil {
			return handleError(c, err)
		}
	}
	if !servesDecisions(router, resolution.ProviderType) {
		return handleError(c, decisionsUnsupportedModelError(c, resolution))
	}

	body, err = s.guardDecisionInput(c, workflow, &req, body)
	if err != nil {
		return handleError(c, err)
	}
	return s.dispatchNativeJSONWithCache(c, workflow, body, func() error {
		eligible := func(_ core.ModelSelector, providerType string) bool {
			return servesDecisions(router, providerType)
		}
		return s.dispatchNativeJSON(c, decisionsRoute, workflow, eligible,
			func(ctx context.Context, selector core.ModelSelector, providerType, providerName string, headers http.Header) (*core.PassthroughResponse, error) {
				return sendDecisions(ctx, router, selector, providerType, providerName, headers, body)
			})
	})
}

func servesDecisions(router core.RoutableDecisions, providerType string) bool {
	return slices.Contains(router.DecisionsProviderTypes(), strings.TrimSpace(providerType))
}

// sendDecisions sends the body to one target under its own model name.
func sendDecisions(
	ctx context.Context,
	router core.RoutableDecisions,
	selector core.ModelSelector,
	providerType, providerName string,
	headers http.Header,
	body []byte,
) (*core.PassthroughResponse, error) {
	forwarded, err := rewriteMessagesModel(body, selector.Model)
	if err != nil {
		return nil, core.NewInvalidRequestError("invalid request body: "+err.Error(), err)
	}
	resp, err := router.Decisions(ctx, providerType, &core.PassthroughRequest{
		Method:       http.MethodPost,
		Endpoint:     decisionsRoute.endpoint,
		Operation:    decisionsRoute.operation,
		Model:        selector.Model,
		Body:         io.NopCloser(bytes.NewReader(forwarded)),
		Headers:      headers,
		ProviderName: providerName,
	})
	return nativeJSONResult(providerType, resp, err)
}

// decisionsUnsupportedModelError explains a request whose model's provider
// has no Decisions API. It is also logged: a virtual model that sends
// decisions to such a provider is an operator mistake the caller cannot fix.
func decisionsUnsupportedModelError(c *echo.Context, resolution *core.RequestModelResolution) error {
	requested := resolution.RequestedQualifiedModel()
	resolved := resolution.ResolvedQualifiedModel()
	slog.Warn("decisions request routed to a provider without a decisions API",
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
		"model %s is served by provider type %s, which has no decisions API; use a decision model such as openai/gpt-6-luna or a jev model",
		target, resolution.ProviderType,
	), nil).WithParam("model")
}

// guardDecisionInput runs the workflow's prompt guardrails over the input
// and returns the body carrying their edits.
func (s *translatedInferenceService) guardDecisionInput(c *echo.Context, workflow *core.Workflow, req *core.DecisionRequest, body []byte) ([]byte, error) {
	patcher, ok := s.translatedRequestPatcher.(gateway.DecisionRequestPatcher)
	if !ok {
		return body, nil
	}
	return guardNativeMember(s, c, workflow, body, "input", req, patcher.PatchDecisionRequest,
		func(r *core.DecisionRequest) json.RawMessage { return r.Input })
}
