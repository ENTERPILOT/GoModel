package openai

import (
	"context"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
)

// RealtimeTarget implements core.RealtimeProvider for OpenAI's realtime websocket
// (wss://api.openai.com/v1/realtime). The endpoint is derived from the configured
// base URL so endpoint overrides and OpenAI-compatible realtime backends work
// without extra config. Bearer auth is injected here and must never be logged.
// A request carrying a CallID attaches to an existing WebRTC/SIP call as a
// sideband channel instead of opening a fresh model session; an intent selects
// the transcription or translation session surface instead of a conversation.
func (p *Provider) RealtimeTarget(ctx context.Context, req *core.RealtimeRequest) (*core.RealtimeTarget, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("model is required for realtime sessions", nil)
	}

	target := &core.RealtimeTarget{}
	var endpoint string
	var err error
	switch {
	case strings.TrimSpace(req.CallID) != "":
		endpoint, err = providers.OpenAIRealtimeAttachURL(p.GetBaseURL(), req.CallID)
	case strings.TrimSpace(req.Model) == "":
		return nil, core.NewInvalidRequestError("model is required for realtime sessions", nil)
	case req.HasIntent(core.RealtimeIntentTranslation):
		// Translation sessions live on their own endpoint and report no usage
		// events, so the gateway meters the audio it relays to price them.
		endpoint, err = providers.OpenAIRealtimeTranslationURL(p.GetBaseURL(), req.Model)
		target.MeterInputAudio = true
	case req.HasIntent(core.RealtimeIntentTranscription):
		// Transcription sessions pick their model via session.update; OpenAI
		// rejects a model query parameter in this mode, so the requested model
		// only routed the request to this provider and is dropped from the URL.
		// Pinning keeps the in-session selection on the routed model.
		endpoint, err = providers.OpenAIRealtimeTranscriptionURL(p.GetBaseURL())
		target.PinSessionModel = strings.TrimSpace(req.Model)
	default:
		endpoint, err = providers.OpenAIRealtimeURL(p.GetBaseURL(), req.Model)
	}
	if err != nil {
		return nil, err
	}
	// Note: the legacy "OpenAI-Beta: realtime=v1" header is intentionally NOT set.
	// The GA endpoint rejects it ("The Realtime Beta API is no longer supported").

	target.URL = endpoint
	target.Headers = p.realtimeAuthHeaders(ctx)
	return target, nil
}

// SupportsRealtimeIntent implements core.RealtimeIntentProvider: OpenAI serves
// both specialized session surfaces — transcription and translation — on every
// realtime endpoint the gateway exposes. Any other intent is unknown here and is
// rejected upstream of the provider rather than served as a conversation.
func (p *Provider) SupportsRealtimeIntent(intent string) bool {
	return core.EqualRealtimeIntent(intent, core.RealtimeIntentTranscription) ||
		core.EqualRealtimeIntent(intent, core.RealtimeIntentTranslation)
}

// RealtimeCallTarget implements core.RealtimeCallProvider for OpenAI's WebRTC SDP
// exchange (POST https://api.openai.com/v1/realtime/calls, or
// /v1/realtime/translations/calls for translation sessions). The gateway appends
// the model query parameter or session form field itself, so the target is the
// bare calls endpoint.
func (p *Provider) RealtimeCallTarget(ctx context.Context, req *core.RealtimeRequest) (*core.RealtimeHTTPTarget, error) {
	return p.realtimeHTTPTarget(ctx, req, "calls")
}

// RealtimeClientSecretTarget implements core.RealtimeCallProvider for minting
// ephemeral realtime client secrets (POST https://api.openai.com/v1/realtime/client_secrets,
// or /v1/realtime/translations/client_secrets for translation sessions).
func (p *Provider) RealtimeClientSecretTarget(ctx context.Context, req *core.RealtimeRequest) (*core.RealtimeHTTPTarget, error) {
	return p.realtimeHTTPTarget(ctx, req, "client_secrets")
}

func (p *Provider) realtimeHTTPTarget(ctx context.Context, req *core.RealtimeRequest, endpoint string) (*core.RealtimeHTTPTarget, error) {
	if req == nil || strings.TrimSpace(req.Model) == "" {
		return nil, core.NewInvalidRequestError("model is required for realtime calls", nil)
	}
	if req.HasIntent(core.RealtimeIntentTranslation) {
		// Translation sessions sign their WebRTC calls and client secrets on the
		// same dedicated surface as their websocket.
		endpoint = "translations/" + endpoint
	}
	target, err := providers.OpenAIRealtimeHTTPURL(p.GetBaseURL(), endpoint)
	if err != nil {
		return nil, err
	}
	return &core.RealtimeHTTPTarget{URL: target, Headers: p.realtimeAuthHeaders(ctx)}, nil
}

// realtimeAuthHeaders picks the next key in the rotation. A realtime session is
// long-lived, so the key is chosen once per session rather than per event.
func (p *Provider) realtimeAuthHeaders(ctx context.Context) http.Header {
	headers := http.Header{}
	if apiKey := p.keys.NextForContext(ctx); apiKey != "" {
		headers.Set("Authorization", "Bearer "+apiKey)
	}
	return headers
}

// Compile-time assertions that OpenAI implements the realtime capabilities.
var (
	_ core.RealtimeProvider       = (*Provider)(nil)
	_ core.RealtimeCallProvider   = (*Provider)(nil)
	_ core.RealtimeIntentProvider = (*Provider)(nil)
)
