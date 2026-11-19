package server

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/requestlog"
	"github.com/enterpilot/gomodel/internal/usage"
)

// moderationService adapts Echo requests to the model-routed moderation
// provider for the OpenAI-compatible /v1/moderations endpoint: validate,
// authorize, enforce budget, route, record usage, and return the JSON
// response.
type moderationService struct {
	modelCallService
	// logBodies mirrors the request logger config. The endpoint is not
	// ingress-managed, so no request snapshot exists for the request log
	// middleware to read the request body from; the service captures it here.
	// The JSON response is captured by the middleware as usual.
	logBodies bool
}

// Moderations handles POST /v1/moderations.
//
// @Summary      Create moderation (content classification)
// @Description  Classifies text, or text and images, against the provider's moderation categories. model defaults to omni-moderation-latest.
// @Tags         moderations
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      core.ModerationRequest  true  "Moderation request"
// @Success      200      {object}  core.ModerationResponse
// @Failure      400      {object}  core.OpenAIErrorEnvelope
// @Failure      401      {object}  core.OpenAIErrorEnvelope
// @Failure      404      {object}  core.OpenAIErrorEnvelope
// @Failure      429      {object}  core.OpenAIErrorEnvelope
// @Failure      502      {object}  core.OpenAIErrorEnvelope
// @Router       /v1/moderations [post]
func (h *Handler) Moderations(c *echo.Context) error {
	return h.moderations().CreateModeration(c)
}

// CreateModeration handles POST /v1/moderations.
func (s *moderationService) CreateModeration(c *echo.Context) error {
	router, ok := s.provider.(core.ModerationProvider)
	if !ok {
		return handleError(c, core.NewInvalidRequestError("moderation is not supported by the current provider router", nil))
	}

	body, _, err := semanticJSONBody(c)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	if s.logBodies {
		requestlog.EnrichEntryWithRawRequestBody(c, body)
	}
	req, err := core.DecodeModerationRequest(body)
	if err != nil {
		return handleError(c, invalidRequestBodyError(c, err))
	}
	if err := core.ValidateModerationRequest(req); err != nil {
		return handleError(c, err)
	}

	ctx, route, err := s.prepare(c, req.Model, req.Provider)
	if err != nil {
		return handleError(c, err)
	}
	// Dispatch on the resolved model: an alias never reaches the provider lookup.
	req.Model, req.Provider = route.selector.Model, route.selector.Provider
	release, err := enforceRateLimit(c, s.rateLimiter, rateLimitRoute{provider: route.providerName, model: route.model})
	if err != nil {
		return handleError(c, err)
	}
	defer release()
	started := time.Now()
	resp, err := router.CreateModeration(ctx, req)
	inferenceTime := time.Since(started)
	if err != nil {
		return handleError(c, err)
	}
	if resp == nil {
		return handleError(c, core.NewProviderError(route.providerName, http.StatusBadGateway,
			"provider "+route.providerName+" returned empty moderation response", nil))
	}
	s.logUsage(ctx, route, func(pricing *core.ModelPricing) *usage.UsageEntry {
		return usage.ExtractFromModerationResponse(resp, route.requestID, route.model, route.providerType, pricing)
	})
	if err := waitForModelSlowdownFactor(ctx, route.slowdown, inferenceTime); err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, resp)
}
