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
// sideband channel instead of opening a fresh model session.
func (p *Provider) RealtimeTarget(_ context.Context, req *core.RealtimeRequest) (*core.RealtimeTarget, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("model is required for realtime sessions", nil)
	}

	var endpoint string
	var err error
	if strings.TrimSpace(req.CallID) != "" {
		endpoint, err = providers.OpenAIRealtimeAttachURL(p.GetBaseURL(), req.CallID)
	} else if strings.TrimSpace(req.Model) == "" {
		return nil, core.NewInvalidRequestError("model is required for realtime sessions", nil)
	} else {
		endpoint, err = providers.OpenAIRealtimeURL(p.GetBaseURL(), req.Model)
	}
	if err != nil {
		return nil, err
	}
	// Note: the legacy "OpenAI-Beta: realtime=v1" header is intentionally NOT set.
	// The GA endpoint rejects it ("The Realtime Beta API is no longer supported").

	return &core.RealtimeTarget{URL: endpoint, Headers: p.realtimeAuthHeaders()}, nil
}

// RealtimeCallTarget implements core.RealtimeCallProvider for OpenAI's WebRTC SDP
// exchange (POST https://api.openai.com/v1/realtime/calls). The gateway appends
// the model query parameter or session form field itself, so the target is the
// bare calls endpoint.
func (p *Provider) RealtimeCallTarget(_ context.Context, req *core.RealtimeRequest) (*core.RealtimeHTTPTarget, error) {
	return p.realtimeHTTPTarget(req, "calls")
}

// RealtimeClientSecretTarget implements core.RealtimeCallProvider for minting
// ephemeral realtime client secrets (POST https://api.openai.com/v1/realtime/client_secrets).
func (p *Provider) RealtimeClientSecretTarget(_ context.Context, req *core.RealtimeRequest) (*core.RealtimeHTTPTarget, error) {
	return p.realtimeHTTPTarget(req, "client_secrets")
}

func (p *Provider) realtimeHTTPTarget(req *core.RealtimeRequest, endpoint string) (*core.RealtimeHTTPTarget, error) {
	if req == nil || strings.TrimSpace(req.Model) == "" {
		return nil, core.NewInvalidRequestError("model is required for realtime calls", nil)
	}
	target, err := providers.OpenAIRealtimeHTTPURL(p.GetBaseURL(), endpoint)
	if err != nil {
		return nil, err
	}
	return &core.RealtimeHTTPTarget{URL: target, Headers: p.realtimeAuthHeaders()}, nil
}

// realtimeAuthHeaders picks the next key in the rotation. A realtime session is
// long-lived, so the key is chosen once per session rather than per event.
func (p *Provider) realtimeAuthHeaders() http.Header {
	headers := http.Header{}
	if apiKey := p.keys.Next(); apiKey != "" {
		headers.Set("Authorization", "Bearer "+apiKey)
	}
	return headers
}

// Compile-time assertions that OpenAI implements the realtime capabilities.
var (
	_ core.RealtimeProvider     = (*Provider)(nil)
	_ core.RealtimeCallProvider = (*Provider)(nil)
)
