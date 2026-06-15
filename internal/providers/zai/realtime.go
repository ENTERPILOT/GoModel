package zai

import (
	"context"
	"net/http"
	"strings"

	"gomodel/internal/core"
	"gomodel/internal/providers"
)

// RealtimeTarget implements core.RealtimeProvider for Z.ai / Zhipu GLM-Realtime,
// whose websocket endpoint (…/api/paas/v4/realtime) and core event schema mirror
// OpenAI's Realtime API. The URL derives from the configured base URL exactly
// like OpenAI's, so region/host overrides (api.z.ai vs open.bigmodel.cn) are
// honored. Bearer auth is injected here and must never be logged.
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

	return &core.RealtimeTarget{URL: endpoint, Headers: headers}, nil
}

// Compile-time assertion that Z.ai implements the realtime capability.
var _ core.RealtimeProvider = (*Provider)(nil)
