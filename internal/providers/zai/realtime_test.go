package zai

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
	const apiKey = "zai-secret-key"
	p := New(providers.ProviderConfig{APIKey: apiKey}, providers.ProviderOptions{}).(*Provider)

	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "glm-realtime"})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(target.URL, "wss://api.z.ai/api/paas/v4/realtime?"), "url = %q, want Z.ai realtime endpoint", target.URL)

	u, err := url.Parse(target.URL)
	require.NoError(t, err)
	got := u.Query().Get("model")
	assert.Equal(t, "glm-realtime", got)
	got = target.Headers.Get("Authorization")
	assert.Equal(t, "Bearer "+apiKey, got)
}

func TestRealtimeTargetFollowsSetBaseURL(t *testing.T) {
	// open.bigmodel.cn region must be honored when configured via ZAI_BASE_URL.
	p := New(providers.ProviderConfig{APIKey: "k"}, providers.ProviderOptions{}).(*Provider)
	p.SetBaseURL("https://open.bigmodel.cn/api/paas/v4")
	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "glm-realtime"})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(target.URL, "wss://open.bigmodel.cn/api/paas/v4/realtime?"), "url = %q, want the configured region host", target.URL)
}

func TestRealtimeTargetNormalizesCodingPlanBase(t *testing.T) {
	// The GLM Coding Plan base (/api/coding/paas/v4) must still resolve to the
	// fixed realtime path /api/paas/v4/realtime, not /api/coding/paas/v4/realtime.
	p := New(providers.ProviderConfig{APIKey: "k"}, providers.ProviderOptions{}).(*Provider)
	p.SetBaseURL("https://api.z.ai/api/coding/paas/v4")
	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "glm-realtime"})
	require.NoError(t, err)

	u, err := url.Parse(target.URL)
	require.NoError(t, err)
	assert.Equal(t, "/api/paas/v4/realtime", u.Path)
}

func TestRealtimeTargetOmitsAuthWhenNoKey(t *testing.T) {
	p := New(providers.ProviderConfig{APIKey: ""}, providers.ProviderOptions{}).(*Provider)
	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "m"})
	require.NoError(t, err)
	_, present := target.Headers["Authorization"]
	assert.False(t, present)
}

func TestRealtimeTargetMissingModel(t *testing.T) {
	p := New(providers.ProviderConfig{APIKey: "k"}, providers.ProviderOptions{}).(*Provider)
	_, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: " "})
	require.Error(t, err)
}
