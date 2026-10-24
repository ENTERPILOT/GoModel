package openai

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordAuthServer serves /models and records every request. statuses, when
// non-empty, are replayed one status code per request so retry behaviour can
// be exercised.
func recordAuthServer(t *testing.T, statuses ...int) (string, *providertest.Capture) {
	t.Helper()

	var attempts atomic.Int32
	server, capture := providertest.Server(t, func(w http.ResponseWriter, _ *http.Request) {
		attempt := int(attempts.Add(1)) - 1
		if attempt < len(statuses) && statuses[attempt] != http.StatusOK {
			w.WriteHeader(statuses[attempt])
			_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	})
	return server.URL, capture
}

// authHeaders returns the Authorization header of every recorded request in
// arrival order.
func authHeaders(capture *providertest.Capture) []string {
	var seen []string
	for _, req := range capture.All() {
		seen = append(seen, req.Header.Get("Authorization"))
	}
	return seen
}

func rotatingProvider(t *testing.T, baseURL string, retry config.RetryConfig, keys ...string) *CompatibleProvider {
	t.Helper()
	opts := providers.ProviderOptions{
		Keys:       providers.NewKeyring(keys...),
		Resilience: config.ResilienceConfig{Retry: retry},
	}
	return NewCompatibleProvider(keys[0], opts, CompatibleProviderConfig{
		ProviderName: "rotating",
		BaseURL:      baseURL,
		SetHeaders:   bearerHeaders,
	})
}

// The core promise: with several keys configured, successive calls authenticate
// with different keys, cycling in the configured order.
func TestCompatibleProvider_RotatesKeysAcrossRequests(t *testing.T) {
	baseURL, capture := recordAuthServer(t)
	provider := rotatingProvider(t, baseURL, config.RetryConfig{}, "k1", "k2", "k3")

	for range 6 {
		_, err := provider.ListModels(context.Background())
		require.NoError(t, err)
	}

	assert.Equal(t, []string{
		"Bearer k1", "Bearer k2", "Bearer k3",
		"Bearer k1", "Bearer k2", "Bearer k3",
	}, authHeaders(capture))
}

// One key must behave exactly as before rotation existed: the same credential
// every time, so upstream prompt caching keeps hitting.
func TestCompatibleProvider_SingleKeyIsStableAcrossRequests(t *testing.T) {
	baseURL, capture := recordAuthServer(t)
	provider := rotatingProvider(t, baseURL, config.RetryConfig{}, "only")

	for range 3 {
		_, err := provider.ListModels(context.Background())
		require.NoError(t, err)
	}

	assert.Equal(t, []string{"Bearer only", "Bearer only", "Bearer only"}, authHeaders(capture))
}

// The header hook runs per HTTP attempt, so a request retried after a 429 is
// re-sent under the next key rather than hammering the throttled one.
func TestCompatibleProvider_RetryUsesNextKey(t *testing.T) {
	baseURL, capture := recordAuthServer(t, http.StatusTooManyRequests, http.StatusOK)
	retry := config.RetryConfig{
		MaxRetries:     2,
		InitialBackoff: time.Millisecond,
		MaxBackoff:     2 * time.Millisecond,
		BackoffFactor:  1,
	}
	provider := rotatingProvider(t, baseURL, retry, "k1", "k2")
	_, err := provider.ListModels(context.Background())
	require.NoError(t, err)

	assert.Equal(t, []string{"Bearer k1", "Bearer k2"}, authHeaders(capture))
}

// Keyless providers must not grow an Authorization header just because the
// rotation machinery is in place.
func TestCompatibleProvider_NoKeysSendsNoCredential(t *testing.T) {
	baseURL, capture := recordAuthServer(t)
	provider := NewCompatibleProvider("", providers.ProviderOptions{}, CompatibleProviderConfig{
		ProviderName: "keyless",
		BaseURL:      baseURL,
		SetHeaders: func(req *http.Request, apiKey string) {
			providers.SetAuthHeaders(req, apiKey, providers.AuthHeaderConfig{
				AuthScheme:     "Bearer ",
				OptionalAPIKey: true,
			})
		},
	})
	_, err := provider.ListModels(context.Background())
	require.NoError(t, err)

	assert.Equal(t, []string{""}, authHeaders(capture))
}
