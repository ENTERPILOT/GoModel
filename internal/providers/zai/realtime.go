package zai

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"gomodel/internal/core"
)

// realtimePath is the fixed GLM-Realtime websocket path. It is the same across
// regions and is independent of the chat base path (paas/v4, coding/paas/v4,
// anthropic, …), so only the host and scheme are taken from the base URL.
const realtimePath = "/api/paas/v4/realtime"

// RealtimeTarget implements core.RealtimeProvider for Z.ai / Zhipu GLM-Realtime,
// whose core event schema mirrors OpenAI's Realtime API. The host (region) comes
// from the configured base URL while the path is pinned to realtimePath so chat
// base variants like the Coding Plan endpoint still resolve correctly. Bearer
// auth is injected here and must never be logged.
func (p *Provider) RealtimeTarget(_ context.Context, req *core.RealtimeRequest) (*core.RealtimeTarget, error) {
	model := ""
	if req != nil {
		model = strings.TrimSpace(req.Model)
	}
	if model == "" {
		return nil, core.NewInvalidRequestError("model is required for realtime sessions", nil)
	}

	endpoint, err := realtimeURL(p.GetBaseURL(), model)
	if err != nil {
		return nil, err
	}

	headers := http.Header{}
	if apiKey := p.keys.Next(); apiKey != "" {
		headers.Set("Authorization", "Bearer "+apiKey)
	}

	return &core.RealtimeTarget{URL: endpoint, Headers: headers}, nil
}

// realtimeURL maps the configured base URL host to the GLM-Realtime endpoint
// wss://<host>/api/paas/v4/realtime?model=... preserving the region host and
// mapping the scheme to ws/wss.
func realtimeURL(baseURL, model string) (string, error) {
	base := strings.TrimSpace(baseURL)
	if base == "" {
		base = defaultBaseURL
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return "", core.NewInvalidRequestError("invalid realtime base url: "+base, err)
	}
	scheme := "wss"
	if strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "ws") {
		scheme = "ws"
	}
	rt := url.URL{Scheme: scheme, Host: u.Host, Path: realtimePath}
	q := url.Values{}
	q.Set("model", model)
	rt.RawQuery = q.Encode()
	return rt.String(), nil
}

// Compile-time assertion that Z.ai implements the realtime capability.
var _ core.RealtimeProvider = (*Provider)(nil)
