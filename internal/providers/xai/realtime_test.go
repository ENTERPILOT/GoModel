package xai

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRealtimeTarget(t *testing.T) {
	const apiKey = "xai-secret-key"
	p := New(providers.ProviderConfig{APIKey: apiKey}, providers.ProviderOptions{}).(*Provider)

	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "grok-voice-latest"})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(target.URL, "wss://api.x.ai/v1/realtime?"), "url = %q, want xAI realtime endpoint", target.URL)

	parsed, err := url.Parse(target.URL)
	require.NoError(t, err)
	got := parsed.Query().Get("model")
	assert.Equal(t, "grok-voice-latest", got)
	got = target.Headers.Get("Authorization")
	assert.Equal(t, "Bearer "+apiKey, got)
	_, err = p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: " "})
	require.Error(t, err)
}

func TestRealtimeTargetOmitsAuthWhenNoKey(t *testing.T) {
	p := New(providers.ProviderConfig{APIKey: ""}, providers.ProviderOptions{}).(*Provider)
	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "m"})
	require.NoError(t, err)
	_, present := target.Headers["Authorization"]
	assert.False(t, present)
}

func TestRealtimeTargetFollowsSetBaseURL(t *testing.T) {
	p := New(providers.ProviderConfig{APIKey: "k"}, providers.ProviderOptions{}).(*Provider)
	p.SetBaseURL("https://custom.x.example/v1")
	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "m"})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(target.URL, "wss://custom.x.example/v1/realtime"), "url = %q, want the SetBaseURL host", target.URL)
}

func TestRealtimeCallTarget(t *testing.T) {
	const apiKey = "xai-secret-key"
	p := New(providers.ProviderConfig{APIKey: apiKey}, providers.ProviderOptions{}).(*Provider)

	target, err := p.RealtimeCallTarget(context.Background(), &core.RealtimeRequest{Model: "grok-voice-latest"})
	require.NoError(t, err)
	assert.Equal(t, "https://api.x.ai/v1/realtime/calls", target.URL)
	got := target.Headers.Get("Authorization")
	assert.Equal(t, "Bearer "+apiKey, got)
	_, err = p.RealtimeCallTarget(context.Background(), &core.RealtimeRequest{Model: " "})
	require.Error(t, err)
}

func TestRealtimeClientSecretTarget(t *testing.T) {
	p := New(providers.ProviderConfig{APIKey: "k"}, providers.ProviderOptions{}).(*Provider)

	target, err := p.RealtimeClientSecretTarget(context.Background(), &core.RealtimeRequest{Model: "grok-voice-latest"})
	require.NoError(t, err)
	assert.Equal(t, "https://api.x.ai/v1/realtime/client_secrets", target.URL)
}

func TestRealtimeTargetAttachesByCallID(t *testing.T) {
	p := New(providers.ProviderConfig{APIKey: "k"}, providers.ProviderOptions{}).(*Provider)

	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "grok-voice-latest", CallID: "rtc_9"})
	require.NoError(t, err)
	assert.Contains(t, target.URL, "call_id=rtc_9")
	assert.NotContains(t, target.URL, "model=", "sideband attach must not carry a model query")
}
