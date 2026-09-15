package openai

import (
	"context"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func realtimeProvider(t *testing.T, apiKey string) *Provider {
	t.Helper()
	p, ok := New(providers.ProviderConfig{APIKey: apiKey}, providers.ProviderOptions{}).(*Provider)
	require.True(t, ok, "New() should return *Provider")
	return p
}

func TestRealtimeTarget(t *testing.T) {
	const apiKey = "sk-secret-key"
	p := realtimeProvider(t, apiKey)

	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "gpt-realtime"})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(target.URL, "wss://api.openai.com/v1/realtime?"), "url = %q, want wss realtime endpoint", target.URL)
	assert.Equal(t, "Bearer "+apiKey, target.Headers.Get("Authorization"))
	// The legacy beta header must NOT be sent: the GA realtime endpoint rejects it.
	assert.Empty(t, target.Headers.Get("OpenAI-Beta"))
}

func TestRealtimeTargetFollowsSetBaseURL(t *testing.T) {
	// Realtime must dial the configured upstream, not a stale default: SetBaseURL
	// (inherited from CompatibleProvider) updates the client, and RealtimeTarget
	// reads the live base URL, so a custom OpenAI-compatible host is honored and
	// the injected key never goes to the wrong host.
	p := realtimeProvider(t, "k")
	p.SetBaseURL("https://custom.example.com/v1")

	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "m"})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(target.URL, "wss://custom.example.com/v1/realtime"), "url = %q, want the SetBaseURL host", target.URL)
}

func TestRealtimeTargetMissingModel(t *testing.T) {
	p := realtimeProvider(t, "k")
	_, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "  "})
	require.Error(t, err)
}

func TestRealtimeTargetOmitsAuthWhenNoKey(t *testing.T) {
	p := realtimeProvider(t, "")
	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "m"})
	require.NoError(t, err)
	assert.NotContains(t, target.Headers, "Authorization")
}

func TestRealtimeTargetAttachesByCallID(t *testing.T) {
	p := realtimeProvider(t, "k")
	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "gpt-realtime", CallID: "rtc_42"})
	require.NoError(t, err)
	assert.Contains(t, target.URL, "call_id=rtc_42")
	assert.NotContains(t, target.URL, "model=", "sideband attach must not carry a model query")
}

func TestRealtimeCallTarget(t *testing.T) {
	const apiKey = "sk-secret-key"
	p := realtimeProvider(t, apiKey)

	target, err := p.RealtimeCallTarget(context.Background(), &core.RealtimeRequest{Model: "gpt-realtime"})
	require.NoError(t, err)
	assert.Equal(t, "https://api.openai.com/v1/realtime/calls", target.URL)
	assert.Equal(t, "Bearer "+apiKey, target.Headers.Get("Authorization"))
	assert.Empty(t, target.Headers.Get("OpenAI-Beta"))
}

func TestRealtimeClientSecretTarget(t *testing.T) {
	p := realtimeProvider(t, "k")

	target, err := p.RealtimeClientSecretTarget(context.Background(), &core.RealtimeRequest{Model: "gpt-realtime"})
	require.NoError(t, err)
	assert.Equal(t, "https://api.openai.com/v1/realtime/client_secrets", target.URL)
}

func TestRealtimeCallTargetMissingModel(t *testing.T) {
	p := realtimeProvider(t, "k")
	_, err := p.RealtimeCallTarget(context.Background(), &core.RealtimeRequest{Model: " "})
	require.Error(t, err)
}

func TestRealtimeCallTargetFollowsSetBaseURL(t *testing.T) {
	p := realtimeProvider(t, "k")
	p.SetBaseURL("https://custom.example.com/v1")

	target, err := p.RealtimeCallTarget(context.Background(), &core.RealtimeRequest{Model: "m"})
	require.NoError(t, err)
	assert.Equal(t, "https://custom.example.com/v1/realtime/calls", target.URL)
}

func TestRealtimeTargetTranslationIntent(t *testing.T) {
	// A translation session dials the dedicated translations endpoint and, unlike
	// a transcription session, keeps the model in the URL. It reports no usage
	// events of its own, so the provider asks the gateway to meter the audio it
	// relays.
	p := realtimeProvider(t, "k")

	for _, intent := range []string{"translation", " Translation "} {
		target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "gpt-realtime-translate", Intent: intent})
		require.NoError(t, err)
		assert.Equal(t, "wss://api.openai.com/v1/realtime/translations?model=gpt-realtime-translate", target.URL, "intent %q", intent)
		assert.True(t, target.MeterInputAudio, "intent %q: the session must be metered", intent)
		// The model is in the URL, so nothing has to be pinned in-session.
		assert.Empty(t, target.PinSessionModel, "intent %q", intent)
		assert.Equal(t, "Bearer k", target.Headers.Get("Authorization"), "intent %q", intent)
	}
}

func TestRealtimeTargetConversationIsNotMetered(t *testing.T) {
	// Conversation sessions report usage in response.done events, so the gateway
	// must not meter (and double-count) their audio.
	p := realtimeProvider(t, "k")

	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "gpt-realtime"})
	require.NoError(t, err)
	assert.False(t, target.MeterInputAudio)
}

func TestRealtimeHTTPTargetsTranslationIntent(t *testing.T) {
	// Translation sessions sign WebRTC calls and mint client secrets on the same
	// dedicated surface as their websocket.
	p := realtimeProvider(t, "k")
	req := &core.RealtimeRequest{Model: "gpt-realtime-translate", Intent: core.RealtimeIntentTranslation}

	call, err := p.RealtimeCallTarget(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "https://api.openai.com/v1/realtime/translations/calls", call.URL)

	secret, err := p.RealtimeClientSecretTarget(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "https://api.openai.com/v1/realtime/translations/client_secrets", secret.URL)
}

func TestRealtimeTargetTranscriptionIntent(t *testing.T) {
	// A transcription session must dial ?intent=transcription without a model
	// parameter: OpenAI rejects transcription models as the session model, and
	// picks the actual model later via session.update. The requested model only
	// routes the request inside the gateway.
	p := realtimeProvider(t, "k")

	for _, intent := range []string{"transcription", " Transcription "} {
		target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "gpt-4o-transcribe", Intent: intent})
		require.NoError(t, err)
		assert.Equal(t, "wss://api.openai.com/v1/realtime?intent=transcription", target.URL, "intent %q", intent)

		// The URL carries no model, so the provider must ask the gateway to pin
		// the session.update model selection to the routed model.
		assert.Equal(t, "gpt-4o-transcribe", target.PinSessionModel, "intent %q", intent)

		// Transcription models usually report usage in their completed event,
		// but a model that omits it would otherwise leave the session free, so
		// the relayed audio has to back it. The gateway meters it only when the
		// session reports nothing, so this never double-bills the common case.
		assert.True(t, target.MeterInputAudio, "intent %q: the session must be metered as a fallback", intent)
	}

	// The model still gates the request: without one there is nothing to route
	// or attribute usage to, transcription intent or not.
	_, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Intent: "transcription"})
	assert.Error(t, err)

	// Unknown intents keep today's conversation-session behavior.
	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "gpt-realtime", Intent: "conversation"})
	require.NoError(t, err)
	assert.Contains(t, target.URL, "model=gpt-realtime")
	assert.Empty(t, target.PinSessionModel)
}

func TestSupportsRealtimeIntent(t *testing.T) {
	p := realtimeProvider(t, "k")
	cases := map[string]bool{
		core.RealtimeIntentTranscription: true,
		core.RealtimeIntentTranslation:   true,
		" Translation ":                  true, // query parameters arrive padded
		"conversation":                   false,
		"dictation":                      false,
		"":                               false,
	}
	for intent, want := range cases {
		assert.Equal(t, want, p.SupportsRealtimeIntent(intent), "intent %q", intent)
	}
}
