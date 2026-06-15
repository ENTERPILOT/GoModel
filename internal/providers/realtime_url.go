package providers

import (
	"net/url"
	"strings"

	"gomodel/internal/core"
)

// OpenAIRealtimeURL derives an OpenAI-style realtime websocket URL from an
// HTTP(S) base URL: https://host/v1 -> wss://host/v1/realtime?model=... It maps
// the scheme to ws/wss and appends the realtime path and model query parameter.
//
// It is shared by providers whose realtime endpoint mirrors OpenAI's exact shape
// (OpenAI, xAI). Providers whose realtime endpoint differs (e.g. Bailian's
// /api-ws/v1/realtime) build their own target instead.
func OpenAIRealtimeURL(baseURL, model string) (string, error) {
	base := strings.TrimSpace(baseURL)
	if base == "" {
		return "", core.NewInvalidRequestError("realtime base url is required", nil)
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
	q.Set("model", strings.TrimSpace(model)) // accept padded input; forward clean (Postel)
	u.RawQuery = q.Encode()
	return u.String(), nil
}
