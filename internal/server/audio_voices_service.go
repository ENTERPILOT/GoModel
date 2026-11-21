package server

import (
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/requestlog"
)

// AudioVoices handles GET /v1/audio/voices.
//
// @Summary      List voices
// @Description  With model, a GoModel extension: the voices the provider behind model offers, each id a valid voice for POST /v1/audio/speech. Without model, OpenAI's own voice list: one cursor page of the custom voices of the OpenAI account.
// @Tags         audio
// @Produce      json
// @Security     BearerAuth
// @Param        model     query     string  false  "Model ID. Omit for OpenAI's own list of custom voices."
// @Param        provider  query     string  false  "Provider routing hint"
// @Param        limit     query     int     false  "Page size, without model only"
// @Param        after     query     string  false  "Voice ID to list after, without model only"
// @Success      200       {object}  core.AudioVoicesResponse
// @Failure      400       {object}  core.OpenAIErrorEnvelope
// @Failure      401       {object}  core.OpenAIErrorEnvelope
// @Failure      404       {object}  core.OpenAIErrorEnvelope
// @Failure      502       {object}  core.OpenAIErrorEnvelope
// @Router       /v1/audio/voices [get]
func (h *Handler) AudioVoices(c *echo.Context) error {
	return h.audio().ListVoices(c)
}

// ListVoices handles GET /v1/audio/voices. Without a model it is OpenAI's own
// voice list (see listCustomVoices). With one it is a GoModel extension that
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
		return s.listCustomVoices(c, query)
	}
	selector, err := s.authorizedSelector(c, model, strings.TrimSpace(query.Get("provider")))
	if err != nil {
		return handleError(c, err)
	}

	ctx, requestID := requestContextWithRequestID(c.Request())
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

// listCustomVoices handles GET /v1/audio/voices without a model, which is
// OpenAI's own voice list: one cursor page of an account's custom voices. It
// goes to the first provider instance that serves the list, or to the one the
// provider parameter names by instance name or type. The request acts on the
// whole provider account, so the caller must be granted the whole provider, as
// on a model-less passthrough call. Without an instance that serves the list,
// a model is what the request lacks.
func (s *audioService) listCustomVoices(c *echo.Context, query url.Values) error {
	router, ok := s.provider.(core.AudioCustomVoicesRoutableProvider)
	if !ok {
		return handleError(c, core.NewInvalidRequestError("model is required", nil).WithParam("model"))
	}
	requested := strings.TrimSpace(query.Get("provider"))
	providerName, ok := s.customVoicesProvider(router.AudioCustomVoiceProviders(), requested)
	if !ok {
		if requested != "" {
			return handleError(c, core.NewAudioCustomVoicesUnsupportedError(requested))
		}
		return handleError(c, core.NewInvalidRequestError("model is required", nil).WithParam("model"))
	}

	ctx, _ := requestContextWithRequestID(c.Request())
	c.SetRequest(c.Request().WithContext(ctx))
	requestlog.EnrichEntryWithResolvedRoute(c, "", s.providerTypeForName(providerName), providerName)
	if s.modelAuthorizer != nil {
		if err := s.modelAuthorizer.ValidateProviderAccess(ctx, providerName); err != nil {
			return handleError(c, err)
		}
	}
	release, err := enforceRateLimit(c, s.rateLimiter, rateLimitRoute{provider: providerName})
	if err != nil {
		return handleError(c, err)
	}
	defer release()

	resp, err := router.ListCustomVoices(ctx, providerName, core.AudioCustomVoicesParams{
		Limit: query.Get("limit"),
		After: query.Get("after"),
	})
	if err != nil {
		return handleError(c, err)
	}
	if resp == nil {
		resp = &core.AudioCustomVoicesResponse{}
	}
	if resp.Object == "" {
		resp.Object = "list"
	}
	if resp.Data == nil {
		resp.Data = []core.AudioCustomVoice{}
	}
	return c.JSON(http.StatusOK, resp)
}

// customVoicesProvider picks the instance to list from among those that serve
// the list: the first one, or the one requested names by instance name or by
// provider type. A configured instance name is never read as a type, so an
// instance that does not serve the list is not swapped for another account.
func (s *audioService) customVoicesProvider(serving []string, requested string) (string, bool) {
	if requested == "" {
		if len(serving) == 0 {
			return "", false
		}
		return serving[0], true
	}
	if slices.Contains(serving, requested) {
		return requested, true
	}
	if s.providerTypeForName(requested) != "" {
		return "", false
	}
	for _, name := range serving {
		if s.providerTypeForName(name) == requested {
			return name, true
		}
	}
	return "", false
}

func (s *audioService) providerTypeForName(providerName string) string {
	if resolver, ok := s.provider.(core.ProviderNameTypeResolver); ok {
		return strings.TrimSpace(resolver.GetProviderTypeForName(providerName))
	}
	return ""
}
