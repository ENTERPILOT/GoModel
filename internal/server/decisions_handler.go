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
// @Description  Available when an openai or jev provider is configured. OpenAI models answer natively; jev models (Jev, Kev) answer through System One, translated both ways. Hosted Jev reads images sent as base64 data URLs; Kev reads text only.
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
	images := decisionInputImages(req.Input)
	if !s.readsImages(resolution.ResolvedSelector, resolution.ProviderType, images) {
		promoted, ok := s.imageReadingFailover(c.Request().Context(), router, workflow, images)
		if !ok {
			return handleError(c, unreadableImagesError(resolution, images))
		}
		workflow = promoted
		storeWorkflow(c, workflow)
	}

	body, err = s.guardDecisionInput(c, workflow, &req, body)
	if err != nil {
		return handleError(c, err)
	}
	return s.dispatchNativeJSONWithCache(c, workflow, body, func() error {
		// A target that cannot read the input, such as a text-only Kev
		// server for a request with images, is skipped rather than tried.
		eligible := func(selector core.ModelSelector, providerType string) bool {
			return servesDecisions(router, providerType) && s.readsImages(selector, providerType, images)
		}
		return s.dispatchNativeJSON(c, decisionsRoute, workflow, eligible,
			func(ctx context.Context, selector core.ModelSelector, providerType, providerName string, headers http.Header) (*core.PassthroughResponse, error) {
				return sendDecisions(ctx, router, selector, providerType, providerName, headers, body)
			})
	})
}

// imageReadingFailover returns workflow routed to the first target of its
// failover chain that serves Decisions and reads images, for a request with
// images whose resolved model reads text only. A chain such as [kev, openai]
// then sends text to Kev and images to OpenAI. A target the caller may not
// use is passed over like one that cannot read the images. The rest of the
// chain still applies, and targets that cannot read images are skipped there
// too.
func (s *translatedInferenceService) imageReadingFailover(ctx context.Context, router core.RoutableDecisions, workflow *core.Workflow, images decisionImages) (*core.Workflow, bool) {
	inference := s.inference()
	for _, selector := range inference.FailoverSelectors(workflow) {
		providerType := inference.ProviderTypeForSelector(selector, "")
		if !servesDecisions(router, providerType) || !s.readsImages(selector, providerType, images) {
			continue
		}
		if s.modelAuthorizer != nil && s.modelAuthorizer.ValidateModelAccess(ctx, selector) != nil {
			continue
		}
		slog.Debug("decision request with images starts at the first failover target that reads them",
			"resolved_model", workflow.Resolution.ResolvedQualifiedModel(),
			"target_model", selector.QualifiedModel(),
		)
		return executedNativeWorkflow(workflow, selector, gateway.ExecutionMeta{
			ProviderType: providerType,
			ProviderName: gateway.ResolvedProviderName(s.provider, selector, ""),
		}), true
	}
	return nil, false
}

// unreadableImagesError explains why the resolved model cannot read the
// request's images, when no failover target can either.
func unreadableImagesError(resolution *core.RequestModelResolution, images decisionImages) error {
	message := fmt.Sprintf("model %q does not read images; send text only, or use a model that reads images, such as openai/gpt-6-luna or a hosted jev model", resolution.RequestedQualifiedModel())
	if images.links && strings.TrimSpace(resolution.ProviderType) == jevProviderType {
		message = fmt.Sprintf("model %q takes images inline: send each image as a base64 data URL (data:image/png;base64,...), not a link", resolution.RequestedQualifiedModel())
	}
	return core.NewInvalidRequestError(message, nil).WithParam("input")
}

// decisionImages describes the images a Decisions input holds.
type decisionImages struct {
	any bool
	// links is set when an image is a URL rather than inline data.
	links bool
}

// decisionImageReaders reports whether a provider instance reads images in a
// Decisions input; the provider router implements it.
type decisionImageReaders interface {
	DecisionsReadImages(providerName string) (readsImages, known bool)
}

// readsImages reports whether the model may read the input's images. A
// provider that says it reads none (a jev provider pointing at a Kev server)
// is refused first, since models configured without listing carry no
// capabilities to check; then a model the catalog explicitly marks as not
// reading images. jev providers take images inline only, so a link is
// refused there. Any other model gets the benefit of the doubt.
func (s *translatedInferenceService) readsImages(selector core.ModelSelector, providerType string, images decisionImages) bool {
	if !images.any {
		return true
	}
	if images.links && strings.TrimSpace(providerType) == jevProviderType {
		return false
	}
	if readers, ok := s.provider.(decisionImageReaders); ok {
		if reads, known := readers.DecisionsReadImages(gateway.ResolvedProviderName(s.provider, selector, "")); known && !reads {
			return false
		}
	}
	catalog, ok := s.provider.(modelCatalog)
	if !ok {
		return true
	}
	model, ok := catalog.LookupModel(selector.QualifiedModel())
	if !ok || model == nil || model.Metadata == nil {
		return true
	}
	vision, known := model.Metadata.Capabilities["vision"]
	return !known || vision
}

// decisionInputImages reports which images a Decisions input holds.
func decisionInputImages(input json.RawMessage) decisionImages {
	var images decisionImages
	var messages []struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(input, &messages) != nil {
		return images
	}
	for _, msg := range messages {
		var parts []struct {
			Type     string `json:"type"`
			ImageURL string `json:"image_url"`
		}
		if json.Unmarshal(msg.Content, &parts) != nil {
			continue
		}
		for _, part := range parts {
			if part.Type != "input_image" {
				continue
			}
			images.any = true
			if !strings.HasPrefix(strings.TrimSpace(part.ImageURL), "data:") {
				images.links = true
			}
		}
	}
	return images
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
