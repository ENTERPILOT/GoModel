package azure

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"gomodel/internal/core"
)

// RealtimeTarget implements core.RealtimeProvider for Azure OpenAI's GPT Realtime
// API, which uses OpenAI's realtime event schema. Azure differs from OpenAI only
// in the dial shape: the websocket lives at <resource>/openai/realtime with the
// deployment and api-version as query parameters, and auth uses the api-key
// header (not Bearer). The api-key is injected here and must never be logged.
func (p *Provider) RealtimeTarget(_ context.Context, req *core.RealtimeRequest) (*core.RealtimeTarget, error) {
	if req == nil || strings.TrimSpace(req.Model) == "" {
		return nil, core.NewInvalidRequestError("model is required for realtime sessions", nil)
	}

	endpoint, err := p.realtimeURL(strings.TrimSpace(req.Model))
	if err != nil {
		return nil, err
	}

	headers := http.Header{}
	if p.apiKey != "" {
		headers.Set("api-key", p.apiKey)
	}

	return &core.RealtimeTarget{URL: endpoint, Headers: headers}, nil
}

// realtimeURL builds wss://<resource>/openai/realtime?api-version=…&deployment=…
// from the configured base URL's resource root. The model selects the Azure
// deployment.
func (p *Provider) realtimeURL(deployment string) (string, error) {
	root := resourceRootBaseURL(p.GetBaseURL())
	u, err := url.Parse(root)
	if err != nil || u.Host == "" {
		return "", core.NewInvalidRequestError("invalid azure realtime base url: "+root, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "https", "wss", "":
		u.Scheme = "wss"
	case "http", "ws":
		u.Scheme = "ws"
	default:
		return "", core.NewInvalidRequestError("unsupported azure realtime base url scheme: "+u.Scheme, nil)
	}
	// Strip any existing /openai[/v1] root so a base already pointing at the
	// OpenAI sub-path doesn't produce /openai/openai/realtime.
	path := strings.TrimRight(u.Path, "/")
	path = strings.TrimSuffix(path, "/openai/v1")
	path = strings.TrimSuffix(path, "/openai")
	u.Path = path + "/openai/realtime"
	q := url.Values{}
	q.Set("api-version", p.apiVersion)
	q.Set("deployment", deployment)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Compile-time assertion that Azure implements the realtime capability.
var _ core.RealtimeProvider = (*Provider)(nil)
