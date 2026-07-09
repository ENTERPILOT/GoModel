package xai

import (
	"context"
	"net/http"
	"strings"

	"gomodel/internal/core"
	"gomodel/internal/providers"
)

// RealtimeTarget implements core.RealtimeProvider for xAI's Voice Agent API
// (wss://api.x.ai/v1/realtime), which is largely OpenAI Realtime API compatible.
// The endpoint shares OpenAI's shape, so the dial URL is derived from the base
// URL the same way. Bearer auth is injected here and must never be logged.
// A request carrying a CallID attaches to an existing WebRTC call as a sideband
// channel, mirroring OpenAI's attach shape.
func (p *Provider) RealtimeTarget(_ context.Context, req *core.RealtimeRequest) (*core.RealtimeTarget, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("model is required for realtime sessions", nil)
	}

	var endpoint string
	var err error
	if strings.TrimSpace(req.CallID) != "" {
		endpoint, err = providers.OpenAIRealtimeAttachURL(p.compat.GetBaseURL(), req.CallID)
	} else if strings.TrimSpace(req.Model) == "" {
		return nil, core.NewInvalidRequestError("model is required for realtime sessions", nil)
	} else {
		endpoint, err = providers.OpenAIRealtimeURL(p.compat.GetBaseURL(), req.Model)
	}
	if err != nil {
		return nil, err
	}

	return &core.RealtimeTarget{URL: endpoint, Headers: p.realtimeAuthHeaders()}, nil
}

// RealtimeCallTarget implements core.RealtimeCallProvider for xAI's WebRTC SDP
// exchange (POST https://api.x.ai/v1/realtime/calls), which mirrors OpenAI's
// shape. Note: xAI gates WebRTC calls per team; unauthorized accounts receive
// an upstream 403 that is relayed as-is.
func (p *Provider) RealtimeCallTarget(_ context.Context, req *core.RealtimeRequest) (*core.RealtimeHTTPTarget, error) {
	return p.realtimeHTTPTarget(req, "calls")
}

// RealtimeClientSecretTarget implements core.RealtimeCallProvider for minting
// ephemeral realtime client secrets (POST https://api.x.ai/v1/realtime/client_secrets).
func (p *Provider) RealtimeClientSecretTarget(_ context.Context, req *core.RealtimeRequest) (*core.RealtimeHTTPTarget, error) {
	return p.realtimeHTTPTarget(req, "client_secrets")
}

func (p *Provider) realtimeHTTPTarget(req *core.RealtimeRequest, endpoint string) (*core.RealtimeHTTPTarget, error) {
	if req == nil || strings.TrimSpace(req.Model) == "" {
		return nil, core.NewInvalidRequestError("model is required for realtime calls", nil)
	}
	target, err := providers.OpenAIRealtimeHTTPURL(p.compat.GetBaseURL(), endpoint)
	if err != nil {
		return nil, err
	}
	return &core.RealtimeHTTPTarget{URL: target, Headers: p.realtimeAuthHeaders()}, nil
}

func (p *Provider) realtimeAuthHeaders() http.Header {
	headers := http.Header{}
	if apiKey := p.keys.Next(); apiKey != "" {
		headers.Set("Authorization", "Bearer "+apiKey)
	}
	return headers
}

// Compile-time assertions that xAI implements the realtime capabilities.
var (
	_ core.RealtimeProvider     = (*Provider)(nil)
	_ core.RealtimeCallProvider = (*Provider)(nil)
)
