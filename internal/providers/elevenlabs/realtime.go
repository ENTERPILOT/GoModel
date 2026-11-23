package elevenlabs

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// Scribe Realtime fixes the session's audio format and commit strategy in the
// websocket URL, before the client can send a session.update. The gateway dials
// with the OpenAI realtime defaults so an OpenAI client works unchanged: PCM16
// mono at 24 kHz, and voice activity detection deciding where a segment ends.
const (
	realtimePath           = "/v1/speech-to-text/realtime"
	realtimeAudioFormat    = "pcm_24000"
	realtimeSampleRate     = 24000
	realtimeCommitStrategy = "vad"
)

// RealtimeTarget implements core.RealtimeProvider for Scribe Realtime, the
// ElevenLabs streaming speech-to-text websocket. Its protocol is not the OpenAI
// realtime event schema, so the target carries a translator that converts the
// session both ways. ElevenLabs reports no usage, so the session is metered from
// the audio the gateway relays. The credential travels in the xi-api-key header
// and must never be logged.
func (p *Provider) RealtimeTarget(ctx context.Context, req *core.RealtimeRequest) (*core.RealtimeTarget, error) {
	if req == nil || strings.TrimSpace(req.Model) == "" {
		return nil, core.NewInvalidRequestError("model is required for realtime sessions", nil)
	}
	if strings.TrimSpace(req.CallID) != "" {
		return nil, core.NewInvalidRequestError("elevenlabs does not support realtime calls", nil)
	}
	model := strings.TrimSpace(req.Model)

	endpoint, err := realtimeURL(p.client.BaseURL(), model)
	if err != nil {
		return nil, err
	}

	headers := http.Header{}
	if apiKey := p.keys.NextForContext(ctx); apiKey != "" {
		headers.Set(authHeader, apiKey)
	}

	return &core.RealtimeTarget{
		URL:             endpoint,
		Headers:         headers,
		HTTPClient:      p.client.HTTPClient(),
		MeterInputAudio: true,
		Translator:      newRealtimeTranslator(model),
	}, nil
}

// SupportsRealtimeIntent implements core.RealtimeIntentProvider. Every Scribe
// Realtime session is a transcription session, with or without the intent.
func (p *Provider) SupportsRealtimeIntent(intent string) bool {
	return core.EqualRealtimeIntent(intent, core.RealtimeIntentTranscription)
}

// realtimeURL maps the configured base URL to the Scribe Realtime websocket:
// https://host -> wss://host/v1/speech-to-text/realtime?model_id=... It keeps
// the host, so a regional or data-residency base URL selects the same region
// for realtime, and any path prefix a reverse proxy adds.
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
	rt := url.URL{Scheme: scheme, Host: u.Host, Path: strings.TrimRight(u.Path, "/") + realtimePath}
	q := url.Values{}
	q.Set("model_id", model)
	q.Set("audio_format", realtimeAudioFormat)
	q.Set("commit_strategy", realtimeCommitStrategy)
	rt.RawQuery = q.Encode()
	return rt.String(), nil
}

// Compile-time assertions that ElevenLabs implements the realtime capabilities.
var (
	_ core.RealtimeProvider       = (*Provider)(nil)
	_ core.RealtimeIntentProvider = (*Provider)(nil)
)
