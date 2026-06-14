package openai

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"gomodel/internal/core"
)

// RealtimeTarget implements core.RealtimeProvider for OpenAI's realtime websocket
// (wss://api.openai.com/v1/realtime). The endpoint is derived from the configured
// base URL so endpoint overrides and OpenAI-compatible realtime backends work
// without extra config. Bearer auth is injected here and must never be logged.
func (p *Provider) RealtimeTarget(_ context.Context, req *core.RealtimeRequest) (*core.RealtimeTarget, error) {
	if req == nil || strings.TrimSpace(req.Model) == "" {
		return nil, core.NewInvalidRequestError("model is required for realtime sessions", nil)
	}

	endpoint, err := realtimeURL(p.baseURL, req.Model)
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

// realtimeURL turns an HTTP(S) base URL such as https://api.openai.com/v1 into
// the realtime websocket URL wss://api.openai.com/v1/realtime?model=... It maps
// the scheme to ws/wss and appends the realtime path and model query parameter.
func realtimeURL(baseURL, model string) (string, error) {
	base := strings.TrimSpace(baseURL)
	if base == "" {
		base = defaultBaseURL
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", core.NewInvalidRequestError("invalid realtime base url: "+err.Error(), err)
	}
	switch strings.ToLower(u.Scheme) {
	case "https", "wss":
		u.Scheme = "wss"
	case "http", "ws":
		u.Scheme = "ws"
	default:
		return "", core.NewInvalidRequestError("unsupported realtime base url scheme: "+u.Scheme, nil)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/realtime"
	q := u.Query()
	q.Set("model", model)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Compile-time assertion that OpenAI implements the realtime capability.
var _ core.RealtimeProvider = (*Provider)(nil)
