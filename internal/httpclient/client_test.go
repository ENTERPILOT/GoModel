package httpclient

import (
	"net/http"
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

			if client == nil {
				t.Fatal("Expected client to be non-nil")
				return
			}

			if client.Transport == nil {
				t.Fatal("Expected transport to be non-nil")
				return
			}

			transport, ok := client.Transport.(*http.Transport)
			require.True(t, ok)

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

	if client == nil {
		t.Fatal("Expected client to be non-nil")
		return
	}

	if client.Transport == nil {
		t.Fatal("Expected transport to be non-nil")
		return
	}

	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)

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
	transport1 := client1.Transport.(*http.Transport)
	transport2 := client2.Transport.(*http.Transport)

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
	transport := client.Transport.(*http.Transport)

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
