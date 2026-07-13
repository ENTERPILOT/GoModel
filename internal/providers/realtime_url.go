package providers

import (
	"net/url"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// OpenAIRealtimeURL derives an OpenAI-style realtime websocket URL from an
// HTTP(S) base URL: https://host/v1 -> wss://host/v1/realtime?model=... It maps
// the scheme to ws/wss and appends the realtime path and model query parameter.
//
// It is shared by providers whose realtime endpoint mirrors OpenAI's exact shape
// (OpenAI, xAI). Providers whose realtime endpoint differs (e.g. Bailian's
// /api-ws/v1/realtime) build their own target instead.
func OpenAIRealtimeURL(baseURL, model string) (string, error) {
	u, err := openAIRealtimeBase(baseURL, "ws", "wss")
	if err != nil {
		return "", err
	}
	q := url.Values{}
	q.Set("model", strings.TrimSpace(model)) // accept padded input; forward clean (Postel)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// OpenAIRealtimeAttachURL derives the websocket URL that attaches to an existing
// realtime call as a sideband channel: https://host/v1 -> wss://host/v1/realtime?call_id=...
// The call already owns a model, so no model parameter is sent.
func OpenAIRealtimeAttachURL(baseURL, callID string) (string, error) {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return "", core.NewInvalidRequestError("call_id is required to attach to a realtime call", nil)
	}
	u, err := openAIRealtimeBase(baseURL, "ws", "wss")
	if err != nil {
		return "", err
	}
	q := url.Values{}
	q.Set("call_id", callID)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// OpenAIRealtimeHTTPURL derives an OpenAI-style realtime HTTP signaling URL from
// an HTTP(S) base URL: https://host/v1 + "calls" -> https://host/v1/realtime/calls.
// It is the HTTP sibling of OpenAIRealtimeURL for the WebRTC SDP exchange and
// client secret endpoints; ws/wss base schemes map back to http/https.
func OpenAIRealtimeHTTPURL(baseURL, endpoint string) (string, error) {
	u, err := openAIRealtimeBase(baseURL, "http", "https")
	if err != nil {
		return "", err
	}
	if endpoint = strings.Trim(strings.TrimSpace(endpoint), "/"); endpoint != "" {
		u.Path += "/" + endpoint
	}
	return u.String(), nil
}

// openAIRealtimeBase parses the base URL, maps insecure/secure schemes to the
// given pair, and appends the /realtime path segment.
func openAIRealtimeBase(baseURL, insecureScheme, secureScheme string) (*url.URL, error) {
	base := strings.TrimSpace(baseURL)
	if base == "" {
		return nil, core.NewInvalidRequestError("realtime base url is required", nil)
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, core.NewInvalidRequestError("invalid realtime base url: "+err.Error(), err)
	}
	switch strings.ToLower(u.Scheme) {
	case "https", "wss":
		u.Scheme = secureScheme
	case "http", "ws":
		u.Scheme = insecureScheme
	default:
		return nil, core.NewInvalidRequestError("unsupported realtime base url scheme: "+u.Scheme, nil)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/realtime"
	u.RawQuery = ""
	return u, nil
}
