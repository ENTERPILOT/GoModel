package bailian

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"gomodel/internal/core"
)

// RealtimeTarget implements core.RealtimeProvider for Alibaba Cloud Bailian /
// DashScope, whose Qwen-Omni-Realtime API follows the OpenAI realtime event
// schema. Unlike chat, the realtime websocket is NOT under /compatible-mode/v1;
// it lives at /api-ws/v1/realtime on the same host, so the host (and thus the
// region selected via BAILIAN_BASE_URL) is taken from the configured base URL.
func (p *Provider) RealtimeTarget(_ context.Context, req *core.RealtimeRequest) (*core.RealtimeTarget, error) {
	if req == nil || strings.TrimSpace(req.Model) == "" {
		return nil, core.NewInvalidRequestError("model is required for realtime sessions", nil)
	}

	endpoint, err := realtimeURL(p.compatible.GetBaseURL(), req.Model)
	if err != nil {
		return nil, err
	}

	headers := http.Header{}
	if apiKey := p.keys.Next(); apiKey != "" {
		headers.Set("Authorization", "Bearer "+apiKey)
	}

	return &core.RealtimeTarget{URL: endpoint, Headers: headers}, nil
}

// realtimeURL maps the DashScope base URL host to the realtime websocket
// endpoint: wss://<host>/api-ws/v1/realtime?model=... It preserves the host
// (e.g. dashscope-intl.aliyuncs.com for the international region) and only maps
// the scheme to ws/wss.
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
	rt := url.URL{Scheme: scheme, Host: u.Host, Path: "/api-ws/v1/realtime"}
	q := url.Values{}
	q.Set("model", strings.TrimSpace(model)) // accept padded input; forward clean (Postel)
	rt.RawQuery = q.Encode()
	return rt.String(), nil
}

// Compile-time assertion that Bailian implements the realtime capability.
var _ core.RealtimeProvider = (*Provider)(nil)
