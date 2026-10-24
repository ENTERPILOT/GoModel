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
	// logBodies mirrors the audit logger config. The endpoints are not
	// ingress-managed, so no request snapshot exists for the audit middleware
	// to read the request body from; the service captures it here instead.
	// Responses are captured here too, as an image gallery rather than the raw
	// JSON, so a base64 payload never hits the generic 1 MB truncation.
	// logBodies is the master switch; logImageInputs / logImageOutputs decide
	// whether uploaded and generated image bytes are stored in the media store
	// or recorded as metadata-only placeholders. media is where the bytes go;
	// nil leaves placeholders.
	logBodies       bool
	logImageInputs  bool
	logImageOutputs bool
	media           *auditlog.MediaCapturer
}

// inputCapture returns the media capture for uploaded images (edit sources
// and masks), or nil when they are not to be stored.
func (s *imageService) inputCapture(c *echo.Context) *auditlog.MediaCapture {
	if !s.logBodies || !s.logImageInputs {
		return nil
	}
	return s.media.For(c)
}

// outputCapture returns the media capture for generated images, or nil when
// they are not to be stored.
func (s *imageService) outputCapture(c *echo.Context) *auditlog.MediaCapture {
	if !s.logBodies || !s.logImageOutputs {
		return nil
	}
	return s.media.For(c)
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
		return handleError(c, core.NewProviderError(route.providerName, http.StatusBadGateway,
			"provider "+route.providerName+" returned empty image response", nil))
	}
	s.logUsage(ctx, route, func(pricing *core.ModelPricing) *usage.UsageEntry {
		return usage.ExtractFromImageResponse(resp, route.requestID, route.model, route.providerType, pricing)
	})
	if err := waitForModelSlowdownFactor(ctx, route.slowdown, inferenceTime); err != nil {
		return handleError(c, err)
	}
	return s.respondImages(c, resp)
}

// respondImages writes the JSON response and, when body logging is on, records
// it in the audit entry as an image body: envelope metadata plus each image
// stored in the media store (gated by logImageOutputs) or a sized placeholder.
func (s *imageService) respondImages(c *echo.Context, resp *core.ImageGenerationResponse) error {
	if s.logBodies {
		auditlog.EnrichEntryWithResponseBody(c, auditlog.BuildImageResponseBody(s.outputCapture(c), resp))
	}
	return c.JSON(http.StatusOK, resp)
}
