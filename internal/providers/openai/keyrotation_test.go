package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/providers"
)

// recordAuthServer serves /models and records the Authorization header of every
// request it receives. status, when non-empty, is replayed one status code per
// request so retry behaviour can be exercised.
func recordAuthServer(t *testing.T, statuses ...int) (*httptest.Server, func() []string) {
	t.Helper()

	var mu sync.Mutex
	var seen []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempt := len(seen)
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()

		if attempt < len(statuses) && statuses[attempt] != http.StatusOK {
			w.WriteHeader(statuses[attempt])
			_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	t.Cleanup(server.Close)

	return server, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
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
	server, seen := recordAuthServer(t)
	provider := rotatingProvider(t, server.URL, config.RetryConfig{}, "k1", "k2", "k3")

	for range 6 {
		if _, err := provider.ListModels(context.Background()); err != nil {
			t.Fatalf("ListModels() error = %v", err)
		}
	}

	want := []string{
		"Bearer k1", "Bearer k2", "Bearer k3",
		"Bearer k1", "Bearer k2", "Bearer k3",
	}
	got := seen()
	if len(got) != len(want) {
		t.Fatalf("got %d requests, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d Authorization = %q, want %q", i+1, got[i], want[i])
		}
	}
}

// One key must behave exactly as before rotation existed: the same credential
// every time, so upstream prompt caching keeps hitting.
func TestCompatibleProvider_SingleKeyIsStableAcrossRequests(t *testing.T) {
	server, seen := recordAuthServer(t)
	provider := rotatingProvider(t, server.URL, config.RetryConfig{}, "only")

	for range 3 {
		if _, err := provider.ListModels(context.Background()); err != nil {
			t.Fatalf("ListModels() error = %v", err)
		}
	}

	for i, auth := range seen() {
		if auth != "Bearer only" {
			t.Errorf("request %d Authorization = %q, want %q", i+1, auth, "Bearer only")
		}
	}
}

// The header hook runs per HTTP attempt, so a request retried after a 429 is
// re-sent under the next key rather than hammering the throttled one.
func TestCompatibleProvider_RetryUsesNextKey(t *testing.T) {
	server, seen := recordAuthServer(t, http.StatusTooManyRequests, http.StatusOK)
	retry := config.RetryConfig{
		MaxRetries:     2,
		InitialBackoff: time.Millisecond,
		MaxBackoff:     2 * time.Millisecond,
		BackoffFactor:  1,
	}
	provider := rotatingProvider(t, server.URL, retry, "k1", "k2")

	if _, err := provider.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}

	got := seen()
	if len(got) != 2 {
		t.Fatalf("got %d attempts, want 2 (one 429 then one retry)", len(got))
	}
	if got[0] != "Bearer k1" {
		t.Errorf("first attempt Authorization = %q, want %q", got[0], "Bearer k1")
	}
	if got[1] != "Bearer k2" {
		t.Errorf("retry Authorization = %q, want %q: the throttled key must not be reused", got[1], "Bearer k2")
	}
}

// Keyless providers must not grow an Authorization header just because the
// rotation machinery is in place.
func TestCompatibleProvider_NoKeysSendsNoCredential(t *testing.T) {
	server, seen := recordAuthServer(t)
	opts := providers.ProviderOptions{}
	provider := NewCompatibleProvider("", opts, CompatibleProviderConfig{
		ProviderName: "keyless",
		BaseURL:      server.URL,
		SetHeaders: func(req *http.Request, apiKey string) {
			providers.SetAuthHeaders(req, apiKey, providers.AuthHeaderConfig{
				AuthScheme:     "Bearer ",
				OptionalAPIKey: true,
			})
		},
	})

	if _, err := provider.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}

	if auth := seen()[0]; auth != "" {
		t.Errorf("Authorization = %q, want no header", auth)
	}
}
