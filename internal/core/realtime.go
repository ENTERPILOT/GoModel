package core

import (
	"context"
	"net/http"
)

// RealtimeRequest carries the resolved parameters for opening a realtime
// (speech-to-speech) websocket session. The model selects the provider; the
// optional Provider hint mirrors the audio endpoints.
type RealtimeRequest struct {
	Model    string
	Provider string
}

// RealtimeTarget describes the upstream websocket a provider exposes for realtime
// sessions. Realtime is a transport concern, not a translation concern: the
// provider's event schema is the wire format, so the gateway only needs the dial
// URL and the credential headers to inject. Headers must never be logged.
type RealtimeTarget struct {
	URL          string
	Headers      http.Header
	Subprotocols []string
}

// RealtimeProvider is implemented by providers that expose an OpenAI-compatible
// realtime websocket endpoint. It is optional, like AudioProvider, so providers
// without realtime support simply omit it.
type RealtimeProvider interface {
	RealtimeTarget(ctx context.Context, req *RealtimeRequest) (*RealtimeTarget, error)
}

// RealtimeRouter resolves a realtime target for a request. The Router implements
// it by routing on the model (optionally constrained by a provider hint), so it
// backs both the typed /v1/realtime route and the /p/{provider}/v1/realtime
// passthrough upgrade.
type RealtimeRouter interface {
	RealtimeTarget(ctx context.Context, req *RealtimeRequest) (*RealtimeTarget, error)
}
