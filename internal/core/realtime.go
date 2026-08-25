package core

import (
	"context"
	"net/http"
	"strings"
)

// Realtime session intents. An empty intent opens a conversation
// (speech-to-speech) session; the named intents select a provider's specialized
// realtime surface, which differs in endpoint, session config, and event
// namespace. The model routes the request inside the gateway either way.
const (
	RealtimeIntentTranscription = "transcription"
	RealtimeIntentTranslation   = "translation"
)

// RealtimeRequest carries the resolved parameters for opening a realtime
// (speech-to-speech) websocket session. The model selects the provider; the
// optional Provider hint mirrors the audio endpoints. CallID, when set, attaches
// to an existing WebRTC/SIP call as a sideband websocket instead of opening a
// fresh model session. Intent, when set, asks the provider for one of the
// specialized session types above.
type RealtimeRequest struct {
	Model    string
	Provider string
	CallID   string
	Intent   string
}

// HasIntent reports whether the request asks for the given session intent.
func (r *RealtimeRequest) HasIntent(intent string) bool {
	if r == nil {
		return false
	}
	return EqualRealtimeIntent(r.Intent, intent)
}

// EqualRealtimeIntent compares two session intents, accepting padded and
// differently cased input (Postel) so every provider compares intents the same
// way.
func EqualRealtimeIntent(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// RealtimeTarget describes the upstream websocket a provider exposes for realtime
// sessions. Realtime is a transport concern, not a translation concern: the
// provider's event schema is the wire format, so the gateway only needs the dial
// URL and the credential headers to inject. Headers must never be logged.
type RealtimeTarget struct {
	URL          string
	Headers      http.Header
	Subprotocols []string
	// PinSessionModel, when non-empty, names the model the gateway must force
	// into the client's session.update frames. Providers set it when the
	// upstream URL carries no model — as in OpenAI transcription sessions —
	// so the session payload, not the URL, selects the model; pinning keeps
	// that selection on the model the caller was authorized for. Providers
	// whose URL fixes the model leave it empty and no frame mapping happens.
	PinSessionModel string
	// MeterInputAudio asks the gateway to bill the session from the input audio
	// it relays, at the model's per-second input rate. Providers set it for
	// session types that report no usage of their own — OpenAI translation
	// sessions emit only transcript and audio deltas, never a usage event — so
	// such a session is recorded with its real duration instead of as free.
	// Sessions that report their own usage leave it false.
	MeterInputAudio bool
}

// RealtimeProvider is implemented by providers that expose an OpenAI-compatible
// realtime websocket endpoint. It is optional, like AudioProvider, so providers
// without realtime support simply omit it.
type RealtimeProvider interface {
	RealtimeTarget(ctx context.Context, req *RealtimeRequest) (*RealtimeTarget, error)
}

// RealtimeIntentProvider is implemented by realtime providers that serve one or
// more specialized session intents. It is optional: a provider that omits it
// serves conversation sessions only, and the gateway rejects a specialized
// intent for its models instead of silently opening a conversation session in
// its place. A provider reports an intent only when every realtime surface it
// exposes honors it — websocket, WebRTC calls, and client secrets alike.
type RealtimeIntentProvider interface {
	SupportsRealtimeIntent(intent string) bool
}

// RealtimeRouter resolves a realtime target for a request. The Router implements
// it by routing on the model (optionally constrained by a provider hint), so it
// backs both the typed /v1/realtime route and the /p/{provider}/v1/realtime
// passthrough upgrade.
type RealtimeRouter interface {
	RealtimeTarget(ctx context.Context, req *RealtimeRequest) (*RealtimeTarget, error)
}

// RealtimeHTTPTarget describes an upstream HTTPS endpoint for realtime call
// signaling (WebRTC SDP exchange, ephemeral client secrets). Like the websocket
// target, it carries only the dial URL and the credential headers to inject;
// headers must never be logged.
type RealtimeHTTPTarget struct {
	URL     string
	Headers http.Header
}

// RealtimeCallProvider is implemented by providers that expose OpenAI-compatible
// realtime HTTP signaling endpoints: SDP exchange for WebRTC calls and ephemeral
// client secrets for browser clients. It is optional, like RealtimeProvider;
// websocket-only realtime providers simply omit it.
type RealtimeCallProvider interface {
	RealtimeCallTarget(ctx context.Context, req *RealtimeRequest) (*RealtimeHTTPTarget, error)
	RealtimeClientSecretTarget(ctx context.Context, req *RealtimeRequest) (*RealtimeHTTPTarget, error)
}

// RealtimeCallRouter resolves realtime HTTP signaling targets for a request. The
// Router implements it by routing on the model, mirroring RealtimeRouter.
type RealtimeCallRouter interface {
	RealtimeCallTarget(ctx context.Context, req *RealtimeRequest) (*RealtimeHTTPTarget, error)
	RealtimeClientSecretTarget(ctx context.Context, req *RealtimeRequest) (*RealtimeHTTPTarget, error)
}
