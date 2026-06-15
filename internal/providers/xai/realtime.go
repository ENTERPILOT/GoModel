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
func (p *Provider) RealtimeTarget(_ context.Context, req *core.RealtimeRequest) (*core.RealtimeTarget, error) {
	if req == nil || strings.TrimSpace(req.Model) == "" {
		return nil, core.NewInvalidRequestError("model is required for realtime sessions", nil)
	}

	endpoint, err := providers.OpenAIRealtimeURL(p.client.BaseURL(), req.Model)
	if err != nil {
		return nil, err
	}

	headers := http.Header{}
	if p.apiKey != "" {
		headers.Set("Authorization", "Bearer "+p.apiKey)
	}

	return &core.RealtimeTarget{URL: endpoint, Headers: headers}, nil
}

// Compile-time assertion that xAI implements the realtime capability.
var _ core.RealtimeProvider = (*Provider)(nil)
