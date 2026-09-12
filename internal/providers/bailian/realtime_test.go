package bailian

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

func TestRealtimeURL(t *testing.T) {
	tests := []struct {
		name     string
		baseURL  string
		model    string
		wantBase string
		wantErr  bool
	}{
		{name: "default mainland host", baseURL: defaultBaseURL, model: "qwen3-omni-flash-realtime", wantBase: "wss://dashscope.aliyuncs.com/api-ws/v1/realtime"},
		{name: "empty falls back to default", baseURL: "", model: "m", wantBase: "wss://dashscope.aliyuncs.com/api-ws/v1/realtime"},
		{name: "international region host preserved", baseURL: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", model: "m", wantBase: "wss://dashscope-intl.aliyuncs.com/api-ws/v1/realtime"},
		{name: "missing host", baseURL: "not-a-url", model: "m", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := realtimeURL(tt.baseURL, tt.model)
			if tt.wantErr {
				require.Error(t, err)

				return
			}
			require.NoError(t, err)

			u, parseErr := url.Parse(got)
			require.NoError(t, parseErr)
			base := u.Scheme + "://" + u.Host + u.Path
			assert.Equal(t, tt.wantBase, base)
			assert.Equal(t, tt.model, u.Query().Get("model"))
		})
	}
}

func TestRealtimeTarget(t *testing.T) {
	const apiKey = "sk-bailian-secret"
	p := New(providers.ProviderConfig{APIKey: apiKey}, providers.ProviderOptions{}).(*Provider)

	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "qwen3-omni-flash-realtime"})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(target.URL, "wss://dashscope.aliyuncs.com/api-ws/v1/realtime?"), "url = %q, want DashScope realtime endpoint", target.URL)
	got := target.Headers.Get("Authorization")
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
	// SetBaseURL switches the DashScope region; the realtime host must follow.
	p := New(providers.ProviderConfig{APIKey: "k"}, providers.ProviderOptions{}).(*Provider)
	p.SetBaseURL("https://dashscope-intl.aliyuncs.com/compatible-mode/v1")
	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "m"})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(target.URL, "wss://dashscope-intl.aliyuncs.com/api-ws/v1/realtime"), "url = %q, want the SetBaseURL region host", target.URL)
}
