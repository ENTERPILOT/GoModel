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
// A request carrying a CallID attaches to an existing WebRTC call as a sideband
// channel on the GA v1 surface (<resource>/openai/v1/realtime?call_id=...),
// which needs no api-version parameter.
func (p *Provider) RealtimeTarget(_ context.Context, req *core.RealtimeRequest) (*core.RealtimeTarget, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("model is required for realtime sessions", nil)
	}

	var endpoint string
	var err error
	if strings.TrimSpace(req.CallID) != "" {
		endpoint, err = p.realtimeAttachURL(strings.TrimSpace(req.CallID))
	} else if strings.TrimSpace(req.Model) == "" {
		return nil, core.NewInvalidRequestError("model is required for realtime sessions", nil)
	} else {
		endpoint, err = p.realtimeURL(strings.TrimSpace(req.Model))
	}
	if err != nil {
		return nil, err
	}

	return &core.RealtimeTarget{URL: endpoint, Headers: p.realtimeAuthHeaders()}, nil
}

// RealtimeCallTarget implements core.RealtimeCallProvider for Azure OpenAI's GA
// WebRTC SDP exchange: POST https://<resource>.openai.azure.com/openai/v1/realtime/calls.
// The GA v1 surface mirrors OpenAI's and takes no api-version parameter; the
// model query parameter selects the Azure deployment.
func (p *Provider) RealtimeCallTarget(_ context.Context, req *core.RealtimeRequest) (*core.RealtimeHTTPTarget, error) {
	return p.realtimeHTTPTarget(req, "calls")
}

// RealtimeClientSecretTarget implements core.RealtimeCallProvider for minting
// ephemeral realtime client secrets on Azure's GA v1 surface:
// POST https://<resource>.openai.azure.com/openai/v1/realtime/client_secrets.
func (p *Provider) RealtimeClientSecretTarget(_ context.Context, req *core.RealtimeRequest) (*core.RealtimeHTTPTarget, error) {
	return p.realtimeHTTPTarget(req, "client_secrets")
}

func (p *Provider) realtimeHTTPTarget(req *core.RealtimeRequest, endpoint string) (*core.RealtimeHTTPTarget, error) {
	if req == nil || strings.TrimSpace(req.Model) == "" {
		return nil, core.NewInvalidRequestError("model is required for realtime calls", nil)
	}
	root, err := p.realtimeGARoot()
	if err != nil {
		return nil, err
	}
	root.Path += "/" + endpoint
	return &core.RealtimeHTTPTarget{URL: root.String(), Headers: p.realtimeAuthHeaders()}, nil
}

// realtimeAttachURL builds the GA sideband attach websocket URL:
// wss://<resource>/openai/v1/realtime?call_id=...
func (p *Provider) realtimeAttachURL(callID string) (string, error) {
	u, err := p.realtimeGARoot()
	if err != nil {
		return "", err
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	u.Path = strings.TrimSuffix(u.Path, "/realtime") + "/realtime"
	q := url.Values{}
	q.Set("call_id", callID)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// realtimeGARoot derives <resource root>/openai/v1/realtime from the configured
// base URL, stripping any existing /openai[/v1] or deployment sub-path first.
func (p *Provider) realtimeGARoot() (*url.URL, error) {
	root := resourceRootBaseURL(p.GetBaseURL())
	u, err := url.Parse(root)
	if err != nil || u.Host == "" {
		return nil, core.NewInvalidRequestError("invalid azure realtime base url: "+root, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "https", "wss", "":
		u.Scheme = "https"
	case "http", "ws":
		u.Scheme = "http"
	default:
		return nil, core.NewInvalidRequestError("unsupported azure realtime base url scheme: "+u.Scheme, nil)
	}
	path := strings.TrimRight(u.Path, "/")
	path = strings.TrimSuffix(path, "/openai/v1")
	path = strings.TrimSuffix(path, "/openai")
	u.Path = path + "/openai/v1/realtime"
	u.RawQuery = ""
	return u, nil
}

func (p *Provider) realtimeAuthHeaders() http.Header {
	headers := http.Header{}
	if p.apiKey != "" {
		headers.Set("api-key", p.apiKey)
	}
	return headers
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

// Compile-time assertions that Azure implements the realtime capabilities.
var (
	_ core.RealtimeProvider     = (*Provider)(nil)
	_ core.RealtimeCallProvider = (*Provider)(nil)
)
