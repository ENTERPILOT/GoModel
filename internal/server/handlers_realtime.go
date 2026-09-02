package server

import (
	"github.com/labstack/echo/v5"
)

// Realtime handles GET /v1/realtime.
//
// @Summary      Open a realtime session
// @Description  Upgrades to a websocket and relays an OpenAI-compatible realtime (speech-to-speech) session to the provider that owns the model named in the ?model= query parameter. Provider credentials are injected by the gateway. Passing ?call_id= instead attaches to an existing WebRTC/SIP call as a sideband channel; calls created through this gateway instance are routed automatically, others need explicit model (and provider) parameters.
// @Tags         realtime
// @Security     BearerAuth
// @Param        model     query     string  false  "Model that owns the realtime session (required unless call_id names a call created through this gateway instance)"
// @Param        provider  query     string  false  "Optional provider hint"
// @Param        call_id   query     string  false  "Existing WebRTC/SIP call to attach to as a sideband channel"
// @Success      101       {string}  string  "Switching Protocols"
// @Failure      400       {object}  core.OpenAIErrorEnvelope
// @Failure      401       {object}  core.OpenAIErrorEnvelope
// @Failure      404       {object}  core.OpenAIErrorEnvelope
// @Failure      429       {object}  core.OpenAIErrorEnvelope
// @Failure      501       {object}  core.OpenAIErrorEnvelope
// @Failure      502       {object}  core.OpenAIErrorEnvelope
// @Router       /v1/realtime [get]
func (h *Handler) Realtime(c *echo.Context) error {
	return h.realtime().Realtime(c)
}

// RealtimeTranslations handles GET /v1/realtime/translations.
//
// @Summary      Open a realtime translation session
// @Description  Upgrades to a websocket and relays an OpenAI-compatible realtime speech translation session (e.g. gpt-realtime-translate) to the provider that owns the model named in the ?model= query parameter. Translation sessions use their own event namespace and set the target language with session.update (session.audio.output.language); provider credentials are injected by the gateway. Equivalent to /v1/realtime?intent=translation.
// @Tags         realtime
// @Security     BearerAuth
// @Param        model     query     string  true   "Translation model that owns the session"
// @Param        provider  query     string  false  "Optional provider hint"
// @Success      101       {string}  string  "Switching Protocols"
// @Failure      400       {object}  core.OpenAIErrorEnvelope
// @Failure      401       {object}  core.OpenAIErrorEnvelope
// @Failure      404       {object}  core.OpenAIErrorEnvelope
// @Failure      429       {object}  core.OpenAIErrorEnvelope
// @Failure      501       {object}  core.OpenAIErrorEnvelope
// @Failure      502       {object}  core.OpenAIErrorEnvelope
// @Router       /v1/realtime/translations [get]
func (h *Handler) RealtimeTranslations(c *echo.Context) error {
	return h.realtime().RealtimeTranslations(c)
}

// RealtimeCalls handles POST /v1/realtime/calls.
//
// @Summary      Create a realtime WebRTC call
// @Description  OpenAI-compatible WebRTC SDP exchange. Accepts a raw application/sdp offer with the model in the ?model= query parameter, or a multipart form with sdp and session (JSON) fields. The gateway routes by model, injects provider credentials, and relays the SDP answer; the Location header carries the created call id. Media flows directly between the client and the provider, so usage is recorded by a gateway-side sideband observer when usage tracking is enabled.
// @Tags         realtime
// @Accept       application/sdp
// @Accept       mpfd
// @Produce      application/sdp
// @Produce      json
// @Security     BearerAuth
// @Param        model     query     string  false  "Model that owns the call (required for application/sdp offers)"
// @Param        provider  query     string  false  "Optional provider hint"
// @Param        request   body      string  true   "SDP offer (raw application/sdp body), or a multipart form with sdp and session (JSON) fields"
// @Success      201       {string}  string  "SDP answer"
// @Failure      400       {object}  core.OpenAIErrorEnvelope
// @Failure      401       {object}  core.OpenAIErrorEnvelope
// @Failure      429       {object}  core.OpenAIErrorEnvelope
// @Failure      501       {object}  core.OpenAIErrorEnvelope
// @Failure      502       {object}  core.OpenAIErrorEnvelope
// @Router       /v1/realtime/calls [post]
func (h *Handler) RealtimeCalls(c *echo.Context) error {
	return h.realtime().RealtimeCalls(c)
}

// RealtimeTranslationCalls handles POST /v1/realtime/translations/calls.
//
// @Summary      Create a realtime translation call (WebRTC)
// @Description  OpenAI-compatible WebRTC SDP exchange for speech translation sessions. Identical to /v1/realtime/calls except that the session runs on the provider's translation surface; the created call is addressed at /v1/realtime/translations/calls/{call_id}.
// @Tags         realtime
// @Accept       application/sdp
// @Accept       mpfd
// @Produce      application/sdp
// @Produce      json
// @Security     BearerAuth
// @Param        model     query     string  false  "Translation model that owns the session (required for raw SDP offers)"
// @Param        provider  query     string  false  "Optional provider hint"
// @Param        request   body      string  true   "SDP offer (raw application/sdp body), or a multipart form with sdp and session (JSON) fields"
// @Success      201       {string}  string  "SDP answer"
// @Failure      400       {object}  core.OpenAIErrorEnvelope
// @Failure      401       {object}  core.OpenAIErrorEnvelope
// @Failure      404       {object}  core.OpenAIErrorEnvelope
// @Failure      429       {object}  core.OpenAIErrorEnvelope
// @Failure      501       {object}  core.OpenAIErrorEnvelope
// @Failure      502       {object}  core.OpenAIErrorEnvelope
// @Router       /v1/realtime/translations/calls [post]
func (h *Handler) RealtimeTranslationCalls(c *echo.Context) error {
	return h.realtime().RealtimeTranslationCalls(c)
}

// RealtimeClientSecrets handles POST /v1/realtime/client_secrets.
//
// @Summary      Mint an ephemeral realtime client secret
// @Description  OpenAI-compatible ephemeral credential minting for browser and mobile realtime clients. Routes by session.model (or the transcription model for transcription sessions), applies the same model-access, budget, and rate-limit gates as other model endpoints, and relays the provider response verbatim. The minted secret authenticates the client directly against the provider, bypassing the gateway for the session itself.
// @Tags         realtime
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        provider  query     string  false  "Optional provider hint"
// @Param        request   body      object{session=object,expires_after=object}  true  "Client secret request: session config with the routing model (session.model, or the nested transcription model) plus optional expires_after; additional fields are relayed verbatim"
// @Success      200       {object}  map[string]any  "Provider client secret response"
// @Failure      400       {object}  core.OpenAIErrorEnvelope
// @Failure      401       {object}  core.OpenAIErrorEnvelope
// @Failure      429       {object}  core.OpenAIErrorEnvelope
// @Failure      501       {object}  core.OpenAIErrorEnvelope
// @Failure      502       {object}  core.OpenAIErrorEnvelope
// @Router       /v1/realtime/client_secrets [post]
func (h *Handler) RealtimeClientSecrets(c *echo.Context) error {
	return h.realtime().RealtimeClientSecrets(c)
}

// RealtimeTranslationClientSecrets handles POST /v1/realtime/translations/client_secrets.
//
// @Summary      Mint an ephemeral realtime translation client secret
// @Description  OpenAI-compatible ephemeral credential minting for browser and mobile clients that open a speech translation session. Identical to /v1/realtime/client_secrets except that the credential is minted on the provider's translation surface.
// @Tags         realtime
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        provider  query     string  false  "Optional provider hint"
// @Param        request   body      object  true   "Realtime translation session config carrying session.model"
// @Success      200       {object}  map[string]interface{}
// @Failure      400       {object}  core.OpenAIErrorEnvelope
// @Failure      401       {object}  core.OpenAIErrorEnvelope
// @Failure      404       {object}  core.OpenAIErrorEnvelope
// @Failure      429       {object}  core.OpenAIErrorEnvelope
// @Failure      501       {object}  core.OpenAIErrorEnvelope
// @Failure      502       {object}  core.OpenAIErrorEnvelope
// @Router       /v1/realtime/translations/client_secrets [post]
func (h *Handler) RealtimeTranslationClientSecrets(c *echo.Context) error {
	return h.realtime().RealtimeTranslationClientSecrets(c)
}
