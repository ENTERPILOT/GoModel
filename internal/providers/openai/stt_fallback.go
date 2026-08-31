package openai

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// sttFallbackModelID is the model advertised for an upstream that serves
// speech-to-text without a model catalog. OpenAI SDK clients default their
// transcription calls to whisper-1, and STT-only servers commonly accept or
// ignore the model field, so this ID makes such providers routable without
// configuration. Operators whose upstream requires a specific model name can
// declare it with the provider's models list instead.
const sttFallbackModelID = "whisper-1"

// speechToTextFallbackModels decides whether the speech-to-text fallback
// applies to a ListModels outcome and returns the synthesized inventory when
// it does. The fallback covers OpenAI-compatible STT servers that expose
// /v1/audio/transcriptions without any model listing: it applies when model
// discovery is enabled for this provider and the upstream either has no
// /models endpoint (listErr says the route is missing) or listed no models
// (listErr is nil), and the transcription endpoint probe confirms the
// upstream serves it.
func (p *CompatibleProvider) speechToTextFallbackModels(ctx context.Context, listErr error) *core.ModelsResponse {
	if !p.detectSpeechToText {
		return nil
	}
	if listErr != nil && !isMissingEndpointError(listErr) {
		return nil
	}
	if !p.hasTranscriptionEndpoint(ctx) {
		return nil
	}
	if p.sttFallbackAnnounced.CompareAndSwap(false, true) {
		slog.Info("upstream lists no models but serves audio transcriptions, advertising fallback speech-to-text model",
			"provider", p.providerName,
			"model", sttFallbackModelID,
		)
	}
	return &core.ModelsResponse{
		Object: "list",
		Data: []core.Model{{
			ID:      sttFallbackModelID,
			Object:  "model",
			OwnedBy: p.providerName,
			Created: time.Now().Unix(),
			Metadata: &core.ModelMetadata{
				Description: "Speech-to-text endpoint detected on an upstream without a model catalog.",
				Modes:       []string{"audio_transcription"},
				Categories:  core.CategoriesForModes([]string{"audio_transcription"}),
			},
		}},
	}
}

// hasTranscriptionEndpoint probes POST /audio/transcriptions with a
// deliberately empty body: no server transcribes anything from it, but one
// that implements the endpoint rejects it with a request-level error (missing
// multipart form, unsupported media type) rather than the route-level
// 404/405/501 an absent endpoint produces. Auth failures (401/403) are not
// accepted as proof: middleware that authenticates before route matching
// emits them for routes that do not exist, and a provider whose credential
// cannot pass the endpoint's own auth could not serve transcriptions anyway.
func (p *CompatibleProvider) hasTranscriptionEndpoint(ctx context.Context) bool {
	_, err := p.client.DoRaw(ctx, p.prepareRequest(llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/audio/transcriptions",
	}))
	if err == nil {
		return true
	}
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		return false
	}
	switch gatewayErr.StatusCode {
	case http.StatusBadRequest,
		http.StatusRequestEntityTooLarge,
		http.StatusUnsupportedMediaType,
		http.StatusUnprocessableEntity:
		return true
	}
	return false
}

// isMissingEndpointError reports whether a provider error means the route is
// not implemented upstream, as opposed to an auth, availability, or network
// failure that says nothing about which endpoints exist.
func isMissingEndpointError(err error) bool {
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		return false
	}
	switch gatewayErr.StatusCode {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return true
	}
	return false
}
