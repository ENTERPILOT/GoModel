package azure

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
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
	u, err := p.realtimeRoot("http", "https")
	if err != nil {
		return nil, err
	}
	u.Path += "/openai/v1/realtime/" + endpoint
	return &core.RealtimeHTTPTarget{URL: u.String(), Headers: p.realtimeAuthHeaders()}, nil
}

// realtimeAttachURL builds the GA sideband attach websocket URL:
// wss://<resource>/openai/v1/realtime?call_id=...
func (p *Provider) realtimeAttachURL(callID string) (string, error) {
	u, err := p.realtimeRoot("ws", "wss")
	if err != nil {
		return "", err
	}
	u.Path += "/openai/v1/realtime"
	q := url.Values{}
	q.Set("call_id", callID)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// realtimeURL builds wss://<resource>/openai/realtime?api-version=…&deployment=…
// from the configured base URL's resource root. The model selects the Azure
// deployment.
func (p *Provider) realtimeURL(deployment string) (string, error) {
	u, err := p.realtimeRoot("ws", "wss")
	if err != nil {
		return "", err
	}
	u.Path += "/openai/realtime"
	q := url.Values{}
	q.Set("api-version", p.apiVersion)
	q.Set("deployment", deployment)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// realtimeRoot derives the resource root from the configured base URL with the
// given scheme pair applied, stripping any existing /openai[/v1] or deployment
// sub-path so callers can append their realtime path without doubling it.
func (p *Provider) realtimeRoot(insecureScheme, secureScheme string) (*url.URL, error) {
	root := resourceRootBaseURL(p.GetBaseURL())
	u, err := url.Parse(root)
	if err != nil || u.Host == "" {
		return nil, core.NewInvalidRequestError("invalid azure realtime base url: "+root, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "https", "wss", "":
		u.Scheme = secureScheme
	case "http", "ws":
		u.Scheme = insecureScheme
	default:
		return nil, core.NewInvalidRequestError("unsupported azure realtime base url scheme: "+u.Scheme, nil)
	}
	path := strings.TrimRight(u.Path, "/")
	path = strings.TrimSuffix(path, "/openai/v1")
	path = strings.TrimSuffix(path, "/openai")
	u.Path = path
	u.RawQuery = ""
	return u, nil
}

func (p *Provider) realtimeAuthHeaders() http.Header {
	headers := http.Header{}
	if apiKey := p.keys.Next(); apiKey != "" {
		headers.Set("api-key", apiKey)
	}
	return headers
}

// Compile-time assertions that Azure implements the realtime capabilities.
var (
	_ core.RealtimeProvider     = (*Provider)(nil)
	_ core.RealtimeCallProvider = (*Provider)(nil)
)
