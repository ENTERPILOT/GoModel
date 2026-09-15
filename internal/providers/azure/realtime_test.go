package azure

import (
	"context"
	"net/url"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRealtimeTarget(t *testing.T) {
	const apiKey = "azure-secret-key"
	p := New(providers.ProviderConfig{
		APIKey:     apiKey,
		BaseURL:    "https://myres.openai.azure.com/openai/deployments/gpt-realtime",
		APIVersion: "2025-04-01-preview",
	}, providers.ProviderOptions{}).(*Provider)

	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "gpt-realtime"})
	require.NoError(t, err)

	u, err := url.Parse(target.URL)
	require.NoError(t, err)
	assert.Equal(t, "wss", u.Scheme)
	assert.Equal(t, "myres.openai.azure.com", u.Host)
	assert.Equal(t, "/openai/realtime", u.Path)
	assert.Equal(t, "gpt-realtime", u.Query().Get("deployment"))
	assert.Equal(t, "2025-04-01-preview", u.Query().Get("api-version"))

	// Azure authenticates with the api-key header, not Bearer.
	assert.Equal(t, apiKey, target.Headers.Get("api-key"))
	assert.Empty(t, target.Headers.Get("Authorization"))
}

func TestRealtimeTargetStripsExistingOpenAIPath(t *testing.T) {
	// A base already rooted at /openai must not yield /openai/openai/realtime.
	for _, base := range []string{
		"https://myres.openai.azure.com/openai",
		"https://myres.openai.azure.com/openai/v1",
	} {
		p := New(providers.ProviderConfig{APIKey: "k", BaseURL: base}, providers.ProviderOptions{}).(*Provider)
		target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "m"})
		require.NoError(t, err)

		u, err := url.Parse(target.URL)
		require.NoError(t, err, "base %q", base)
		assert.Equal(t, "/openai/realtime", u.Path, "base %q", base)
	}
}

func TestRealtimeTargetOmitsAuthWhenNoKey(t *testing.T) {
	p := New(providers.ProviderConfig{
		APIKey:  "",
		BaseURL: "https://myres.openai.azure.com",
	}, providers.ProviderOptions{}).(*Provider)
	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "m"})
	require.NoError(t, err)
	_, present := target.Headers["Api-Key"]
	assert.False(t, present)
}

func TestRealtimeTargetMissingModel(t *testing.T) {
	p := New(providers.ProviderConfig{APIKey: "k", BaseURL: "https://myres.openai.azure.com"}, providers.ProviderOptions{}).(*Provider)
	_, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: " "})
	require.Error(t, err)
}

func TestRealtimeCallTarget(t *testing.T) {
	const apiKey = "azure-secret-key"
	// Base URLs pointing at a deployment or the openai sub-path must all
	// resolve to the GA resource-root calls endpoint.
	for _, base := range []string{
		"https://myres.openai.azure.com/openai/deployments/gpt-realtime",
		"https://myres.openai.azure.com/openai",
		"https://myres.openai.azure.com/openai/v1",
		"https://myres.openai.azure.com",
	} {
		p := New(providers.ProviderConfig{APIKey: apiKey, BaseURL: base}, providers.ProviderOptions{}).(*Provider)
		target, err := p.RealtimeCallTarget(context.Background(), &core.RealtimeRequest{Model: "gpt-realtime"})
		require.NoError(t, err)
		assert.Equal(t, "https://myres.openai.azure.com/openai/v1/realtime/calls", target.URL, "base %q", base)

		// The GA v1 surface takes no api-version parameter.
		assert.NotContains(t, target.URL, "api-version", "base %q", base)
		assert.Equal(t, apiKey, target.Headers.Get("api-key"), "base %q", base)
		assert.Empty(t, target.Headers.Get("Authorization"), "base %q: Azure uses api-key, not Authorization", base)
	}
}

func TestRealtimeClientSecretTarget(t *testing.T) {
	p := New(providers.ProviderConfig{
		APIKey:  "k",
		BaseURL: "https://myres.openai.azure.com/openai/deployments/gpt-realtime",
	}, providers.ProviderOptions{}).(*Provider)

	target, err := p.RealtimeClientSecretTarget(context.Background(), &core.RealtimeRequest{Model: "gpt-realtime"})
	require.NoError(t, err)
	assert.Equal(t, "https://myres.openai.azure.com/openai/v1/realtime/client_secrets", target.URL)
}

func TestRealtimeCallTargetMissingModel(t *testing.T) {
	p := New(providers.ProviderConfig{APIKey: "k", BaseURL: "https://myres.openai.azure.com"}, providers.ProviderOptions{}).(*Provider)
	_, err := p.RealtimeCallTarget(context.Background(), &core.RealtimeRequest{Model: " "})
	require.Error(t, err)
}

func TestRealtimeTargetAttachesByCallID(t *testing.T) {
	p := New(providers.ProviderConfig{
		APIKey:  "k",
		BaseURL: "https://myres.openai.azure.com/openai/deployments/gpt-realtime",
	}, providers.ProviderOptions{}).(*Provider)

	target, err := p.RealtimeTarget(context.Background(), &core.RealtimeRequest{Model: "gpt-realtime", CallID: "rtc_3"})
	require.NoError(t, err)

	u, err := url.Parse(target.URL)
	require.NoError(t, err)
	assert.Equal(t, "wss", u.Scheme)
	assert.Equal(t, "myres.openai.azure.com", u.Host)
	assert.Equal(t, "/openai/v1/realtime", u.Path)
	assert.Equal(t, "rtc_3", u.Query().Get("call_id"))

	// The GA attach surface takes neither api-version nor deployment.
	assert.False(t, u.Query().Has("api-version"))
	assert.False(t, u.Query().Has("deployment"), "query = %q, want only call_id", u.RawQuery)
}
