package server

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/usage"
)

// imageService adapts Echo requests to the model-routed image provider for the
// OpenAI-compatible /v1/images/generations endpoint. It stays a thin transport
// layer: validate, authorize, enforce budget, route, and return the JSON
// response.
type imageService struct {
	modelCallService
	// logBodies mirrors the audit logger config. The endpoint is not
	// ingress-managed, so no request snapshot exists for the audit middleware
	// to read the request body from; the service captures it here instead.
	// The JSON response is captured by the middleware as usual.
	logBodies bool
}

func (s *imageService) router() (core.ImageProvider, error) {
	router, ok := s.provider.(core.ImageProvider)
	if !ok {
		return nil, core.NewInvalidRequestError("image generation is not supported by the current provider router", nil)
	}
	return router, nil
}

// CreateImage handles POST /v1/images/generations.
func (s *imageService) CreateImage(c *echo.Context) error {
	router, err := s.router()
	if err != nil {
		return handleError(c, err)
	}

	body, env, err := semanticJSONBody(c)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	if s.logBodies {
		auditlog.EnrichEntryWithRawRequestBody(c, body)
	}
	req, err := core.DecodeImageGenerationRequest(body, env)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	if err := core.ValidateImageGenerationRequest(req); err != nil {
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
	resp, err := router.CreateImage(ctx, req)
	inferenceTime := time.Since(started)
	if err != nil {
		return handleError(c, err)
	}
	if resp == nil {
		return handleError(c, core.NewProviderError("", http.StatusBadGateway, "provider returned empty image response", nil))
	}
	s.logUsage(ctx, route, func(pricing *core.ModelPricing) *usage.UsageEntry {
		return usage.ExtractFromImageResponse(resp, route.requestID, route.model, route.providerType, pricing)
	})
	if err := waitForModelSlowdownFactor(ctx, route.slowdown, inferenceTime); err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, resp)
}
