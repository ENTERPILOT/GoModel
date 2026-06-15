package openai

import (
	"context"
	"net/http"
	"strings"

	"gomodel/internal/core"
	"gomodel/internal/providers"
)

// RealtimeTarget implements core.RealtimeProvider for OpenAI's realtime websocket
// (wss://api.openai.com/v1/realtime). The endpoint is derived from the configured
// base URL so endpoint overrides and OpenAI-compatible realtime backends work
// without extra config. Bearer auth is injected here and must never be logged.
func (p *Provider) RealtimeTarget(_ context.Context, req *core.RealtimeRequest) (*core.RealtimeTarget, error) {
	if req == nil || strings.TrimSpace(req.Model) == "" {
		return nil, core.NewInvalidRequestError("model is required for realtime sessions", nil)
	}

	endpoint, err := providers.OpenAIRealtimeURL(p.GetBaseURL(), req.Model)
	if err != nil {
		return nil, err
	}

	headers := http.Header{}
	if p.apiKey != "" {
		headers.Set("Authorization", "Bearer "+p.apiKey)
	}
	// Note: the legacy "OpenAI-Beta: realtime=v1" header is intentionally NOT set.
	// The GA endpoint rejects it ("The Realtime Beta API is no longer supported").

	return &core.RealtimeTarget{URL: endpoint, Headers: headers}, nil
}

// Compile-time assertion that OpenAI implements the realtime capability.
var _ core.RealtimeProvider = (*Provider)(nil)
