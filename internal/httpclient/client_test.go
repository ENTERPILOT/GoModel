package httpclient

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	assert.Equal(t, 100, config.MaxIdleConns)
	assert.Equal(t, 100, config.MaxIdleConnsPerHost)
	assert.Equal(t, 90*time.Second, config.IdleConnTimeout)

	// Default timeout is 600s (10 minutes) to match OpenAI/Anthropic SDKs
	assert.Equal(t, 600*time.Second, config.Timeout)
	assert.Equal(t, 30*time.Second, config.DialTimeout)
	assert.Equal(t, 30*time.Second, config.KeepAlive)
	assert.Equal(t, 10*time.Second, config.TLSHandshakeTimeout)

	// Default ResponseHeaderTimeout is 600s (10 minutes) to match OpenAI/Anthropic SDKs
	assert.Equal(t, 600*time.Second, config.ResponseHeaderTimeout)
}

func TestDefaultConfigWithEnvOverrides(t *testing.T) {
	// Set environment variables using plain integers (seconds)
	t.Setenv("HTTP_TIMEOUT", "120")
	t.Setenv("HTTP_RESPONSE_HEADER_TIMEOUT", "90")

	config := DefaultConfig()

	assert.Equal(t, 120*time.Second, config.Timeout)
	assert.Equal(t, 90*time.Second, config.ResponseHeaderTimeout)

	// Other values should remain unchanged
	assert.Equal(t, 30*time.Second, config.DialTimeout)
}

func TestDefaultConfigWithDurationFormat(t *testing.T) {
	// Test Go duration format still works
	t.Setenv("HTTP_TIMEOUT", "2m")

	config := DefaultConfig()

	assert.Equal(t, 2*time.Minute, config.Timeout)
}

func TestDefaultConfigWithInvalidEnv(t *testing.T) {
	// Set invalid environment variable
	t.Setenv("HTTP_TIMEOUT", "invalid")

	config := DefaultConfig()

	// Should fall back to default value
	assert.Equal(t, 600*time.Second, config.Timeout)
}

func TestNewHTTPClient(t *testing.T) {
	tests := []struct {
		name   string
		config *ClientConfig
	}{
		{
			name:   "nil config uses defaults",
			config: nil,
		},
		{
			name: "custom config",
			config: &ClientConfig{
				MaxIdleConns:          50,
				MaxIdleConnsPerHost:   25,
				IdleConnTimeout:       60 * time.Second,
				Timeout:               15 * time.Second,
				DialTimeout:           10 * time.Second,
				KeepAlive:             15 * time.Second,
				TLSHandshakeTimeout:   5 * time.Second,
				ResponseHeaderTimeout: 5 * time.Second,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewHTTPClient(tt.config)

			require.NotNil(t, client, "Expected client to be non-nil")

			require.NotNil(t, client.Transport, "Expected transport to be non-nil")

			transport := underlyingTransport(t, client)

			expectedConfig := tt.config
			if expectedConfig == nil {
				cfg := DefaultConfig()
				expectedConfig = &cfg
			}

			// Verify transport settings
			assert.Equal(t, expectedConfig.MaxIdleConns, transport.MaxIdleConns)
			assert.Equal(t, expectedConfig.MaxIdleConnsPerHost, transport.MaxIdleConnsPerHost)
			assert.Equal(t, expectedConfig.IdleConnTimeout, transport.IdleConnTimeout)
			assert.Equal(t, expectedConfig.Timeout, client.Timeout)
			assert.Equal(t, expectedConfig.TLSHandshakeTimeout, transport.TLSHandshakeTimeout)
			assert.Equal(t, expectedConfig.ResponseHeaderTimeout, transport.ResponseHeaderTimeout)

			// Verify ForceAttemptHTTP2 is enabled
			assert.True(t, transport.ForceAttemptHTTP2)

			// Verify Proxy is set
			assert.NotNil(t, transport.Proxy)
		})
	}
}

func TestNewDefaultHTTPClient(t *testing.T) {
	client := NewDefaultHTTPClient()

	require.NotNil(t, client, "Expected client to be non-nil")

	require.NotNil(t, client.Transport, "Expected transport to be non-nil")

	transport := underlyingTransport(t, client)

	defaultConfig := DefaultConfig()

	// Verify it uses default configuration
	assert.Equal(t, defaultConfig.MaxIdleConns, transport.MaxIdleConns)
	assert.Equal(t, defaultConfig.MaxIdleConnsPerHost, transport.MaxIdleConnsPerHost)
	assert.Equal(t, defaultConfig.Timeout, client.Timeout)
}

func TestHTTPClientIsReusable(t *testing.T) {
	// Test that multiple calls return different client instances (not a singleton)
	// but with the same configuration
	client1 := NewDefaultHTTPClient()
	client2 := NewDefaultHTTPClient()

	assert.NotSame(t, client2, client1)

	// But they should have the same configuration
	transport1 := underlyingTransport(t, client1)
	transport2 := underlyingTransport(t, client2)

	assert.Equal(t, transport2.MaxIdleConns, transport1.MaxIdleConns)
	assert.Equal(t, client2.Timeout, client1.Timeout)
}

func TestClientConfigZeroValues(t *testing.T) {
	// Test that zero values in config are still applied (not replaced with defaults)
	config := &ClientConfig{
		MaxIdleConns:          0,
		MaxIdleConnsPerHost:   0,
		IdleConnTimeout:       0,
		Timeout:               0,
		DialTimeout:           0,
		KeepAlive:             0,
		TLSHandshakeTimeout:   0,
		ResponseHeaderTimeout: 0,
	}

	client := NewHTTPClient(config)
	transport := underlyingTransport(t, client)

	// Zero values should be preserved (not replaced with defaults)
	assert.Equal(t, 0, transport.MaxIdleConns)
	assert.Equal(t, time.Duration(0), client.Timeout)
}

func TestDefaultConfigTimeoutPrecedence(t *testing.T) {
	t.Cleanup(func() { SetConfiguredTimeouts(0, 0) })

	// Built-in default when nothing is configured.
	SetConfiguredTimeouts(0, 0)
	got := DefaultConfig().Timeout
	require.Equal(t, 600*time.Second, got)

	// Config-file values apply when no env override is present.
	SetConfiguredTimeouts(30, 40)
	cfg := DefaultConfig()
	require.Equal(t, 30*time.Second, cfg.Timeout)
	require.Equal(t, 40*time.Second, cfg.ResponseHeaderTimeout)

	// Env vars win over config-file values.
	t.Setenv("HTTP_TIMEOUT", "50")
	t.Setenv("HTTP_RESPONSE_HEADER_TIMEOUT", "60")
	cfg = DefaultConfig()
	require.Equal(t, 50*time.Second, cfg.Timeout)
	require.Equal(t, 60*time.Second, cfg.ResponseHeaderTimeout)

	// Non-positive values clear back to the built-in default.
	t.Setenv("HTTP_TIMEOUT", "")
	SetConfiguredTimeouts(-1, 0)
	got = DefaultConfig().Timeout
	require.Equal(t, 600*time.Second, got)
}

// tokenTransport adds a credential on every round trip, as the Google OAuth
// transport does, so stripping headers in CheckRedirect could not stop it.
type tokenTransport struct{ base http.RoundTripper }

func (t tokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer ya29.token")
	return t.base.RoundTrip(req)
}

// A redirect to another origin is handed back unfollowed, so neither the
// provider credential, a token a wrapping transport adds, nor the request
// body reaches the other host. Same-origin redirects are still followed.
func TestNewHTTPClientDoesNotFollowCrossOriginRedirects(t *testing.T) {
	var otherHits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { otherHits.Add(1) }))
	defer other.Close()

	received := make(chan http.Header, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/next", func(w http.ResponseWriter, r *http.Request) { received <- r.Header.Clone() })
	server := httptest.NewServer(mux)
	defer server.Close()
	serverURL, err := url.Parse(server.URL)
	require.NoError(t, err)
	// localhost reaches the same listener as 127.0.0.1 but is another host.
	mux.Handle("/other-host", http.RedirectHandler("http://localhost:"+serverURL.Port()+"/next", http.StatusTemporaryRedirect))
	mux.Handle("/other-port", http.RedirectHandler(other.URL+"/next", http.StatusPermanentRedirect))
	mux.Handle("/same-origin", http.RedirectHandler("/next", http.StatusTemporaryRedirect))

	tests := []struct {
		name       string
		method     string
		path       string
		transport  func(http.RoundTripper) http.RoundTripper
		wantStatus int
	}{
		{name: "other host", method: http.MethodGet, path: "/other-host", wantStatus: http.StatusTemporaryRedirect},
		{name: "other port with body", method: http.MethodPost, path: "/other-port", wantStatus: http.StatusPermanentRedirect},
		{
			name: "token-adding transport", method: http.MethodPost, path: "/other-port", wantStatus: http.StatusPermanentRedirect,
			transport: func(base http.RoundTripper) http.RoundTripper { return tokenTransport{base: base} },
		},
		{name: "same origin", method: http.MethodGet, path: "/same-origin", wantStatus: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			otherHits.Store(0)
			client := NewHTTPClient(nil)
			if tt.transport != nil {
				client.Transport = tt.transport(client.Transport)
			}
			req, err := http.NewRequestWithContext(t.Context(), tt.method, server.URL+tt.path, strings.NewReader(`{"prompt":"secret"}`))
			require.NoError(t, err)
			req.Header.Set("X-Api-Key", "sk-ant")

			resp, err := client.Do(req)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			assert.Equal(t, tt.wantStatus, resp.StatusCode)

			if tt.wantStatus == http.StatusOK {
				got := <-received
				assert.Equal(t, "sk-ant", got.Get("X-Api-Key"), "same-origin redirect keeps the credential")
				return
			}
			assert.Zero(t, otherHits.Load(), "cross-origin target was contacted")
			select {
			case <-received:
				assert.Fail(t, "cross-origin redirect was followed")
			default:
			}
		})
	}
}

// underlyingTransport unwraps the *http.Transport a client currently uses.
func underlyingTransport(t *testing.T, client *http.Client) *http.Transport {
	t.Helper()
	transport, ok := CurrentTransport(client)
	require.True(t, ok, "Transport is %T, want *dynamicTransport", client.Transport)
	return transport
}

func TestStopCrossOriginRedirects(t *testing.T) {
	tests := []struct {
		from, to string
		wantStop bool
	}{
		{from: "https://api.example.com/v1/x", to: "https://api.example.com/v1/y"},
		{from: "https://api.example.com/v1/x", to: "https://API.example.com/v1/y"},
		{from: "https://api.example.com/v1/x", to: "http://api.example.com/v1/y", wantStop: true},
		{from: "https://api.example.com/v1/x", to: "https://api.example.com:8443/v1/y", wantStop: true},
		{from: "https://api.example.com/v1/x", to: "https://files.example.com/v1/y", wantStop: true},
		{from: "https://api.example.com/v1/x", to: "https://evil.test/", wantStop: true},
	}
	for _, tt := range tests {
		t.Run(tt.to, func(t *testing.T) {
			from := httptest.NewRequest(http.MethodGet, tt.from, nil)
			to := httptest.NewRequest(http.MethodGet, tt.to, nil)
			err := stopCrossOriginRedirects(to, []*http.Request{from})
			if tt.wantStop {
				assert.ErrorIs(t, err, http.ErrUseLastResponse)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestNewHTTPClientStopsAfterTenRedirects(t *testing.T) {
	server := httptest.NewServer(http.RedirectHandler("/loop", http.StatusFound))
	defer server.Close()

	resp, err := NewHTTPClient(nil).Get(server.URL + "/loop")
	if resp != nil {
		require.NoError(t, resp.Body.Close())
	}
	require.ErrorContains(t, err, "stopped after 10 redirects")
}
