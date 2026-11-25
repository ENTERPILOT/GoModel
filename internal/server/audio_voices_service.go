package server

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/requestlog"
)

// AudioVoices handles GET /v1/audio/voices.
//
// @Summary      List voices for a text-to-speech model
// @Description  GoModel extension, not part of the OpenAI API. Returns the voices the provider behind model offers; each id is a valid voice for POST /v1/audio/speech.
// @Tags         audio
// @Produce      json
// @Security     BearerAuth
// @Param        model     query     string  true   "Model ID"
// @Param        provider  query     string  false  "Provider routing hint"
// @Success      200       {object}  core.AudioVoicesResponse
// @Failure      400       {object}  core.OpenAIErrorEnvelope
// @Failure      401       {object}  core.OpenAIErrorEnvelope
// @Failure      404       {object}  core.OpenAIErrorEnvelope
// @Failure      502       {object}  core.OpenAIErrorEnvelope
// @Router       /v1/audio/voices [get]
func (h *Handler) AudioVoices(c *echo.Context) error {
	return h.audio().ListVoices(c)
}

// ListVoices handles GET /v1/audio/voices?model=..., a GoModel extension that
// self-hosted TTS servers also serve. The model resolves and is authorized
// exactly as on /v1/audio/speech, so a caller only sees the voices of a model
// it may synthesize with. It is an unbilled read: no budget or usage entry
// applies. Each call asks the upstream, so request rate limits apply as on
// /v1/audio/speech.
func (s *audioService) ListVoices(c *echo.Context) error {
	lister, ok := s.provider.(core.AudioVoicesProvider)
	if !ok {
		return handleError(c, core.NewInvalidRequestError("listing voices is not supported by the current provider router", nil))
	}
	// Not a model-interaction path, so the snapshot middleware has not lifted
	// the user-path header into the context; do it here as /v1/models does,
	// so model access rules see the same path speech would.
	if ok, err := applyUserPathHeaderToContext(c); !ok {
		return err
	}

	query := c.Request().URL.Query()
	model := strings.TrimSpace(query.Get("model"))
	if model == "" {
		return handleError(c, core.NewInvalidRequestError("model is required", nil).WithParam("model"))
	}
	providerHint := strings.TrimSpace(query.Get("provider"))
	selector, vmRoute, err := s.authorizedSelector(c, model, providerHint)
	if err != nil {
		return handleError(c, err)
	}

	ctx, requestID := requestContextWithRequestID(c.Request())
	requested := core.NewRequestedModelSelector(model, providerHint)
	ctx = withModelTimeouts(ctx, s.modelResolver, requested, selector, vmRoute)
	ctx = withModelRetryRules(ctx, s.modelResolver, requested, selector, vmRoute)
	c.SetRequest(c.Request().WithContext(ctx))
	requestlog.EnrichEntry(c, selector.Model, "")
	route := s.routeFor(selector, requestID)
	requestlog.EnrichEntryWithResolvedRoute(c, selector.QualifiedModel(), route.providerType, route.providerName)
	release, err := enforceRateLimit(c, s.rateLimiter, rateLimitRoute{provider: route.providerName, model: route.model})
	if err != nil {
		return handleError(c, err)
	}
	defer release()

	resp, err := lister.ListVoices(ctx, &core.AudioVoicesRequest{Model: selector.Model, Provider: selector.Provider})
	if err != nil {
		return handleError(c, err)
	}
	if resp == nil || resp.Voices == nil {
		resp = &core.AudioVoicesResponse{Voices: []core.AudioVoice{}}
	}
	return c.JSON(http.StatusOK, resp)
}
