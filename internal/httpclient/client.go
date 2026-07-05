// Package httpclient provides a centralized HTTP client factory with unified configuration.
package httpclient

import (
	"net"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

// ClientConfig holds configuration options for creating HTTP clients
type ClientConfig struct {
	// MaxIdleConns controls the maximum number of idle (keep-alive) connections across all hosts
	MaxIdleConns int

	// MaxIdleConnsPerHost controls the maximum idle (keep-alive) connections to keep per-host
	MaxIdleConnsPerHost int

	// IdleConnTimeout is the maximum amount of time an idle (keep-alive) connection will remain idle before closing itself
	IdleConnTimeout time.Duration

	// Timeout specifies a time limit for requests made by the client
	Timeout time.Duration

	// DialTimeout is the maximum amount of time a dial will wait for a connect to complete
	DialTimeout time.Duration

	// KeepAlive specifies the interval between keep-alive probes for an active network connection
	KeepAlive time.Duration

	// TLSHandshakeTimeout specifies the maximum amount of time to wait for a TLS handshake
	TLSHandshakeTimeout time.Duration

	// ResponseHeaderTimeout specifies the amount of time to wait for a server's response headers
	ResponseHeaderTimeout time.Duration
}

// getEnvDuration reads a duration from an environment variable, returning the default if not set or invalid.
// Accepts either plain integers (interpreted as seconds) or Go duration strings (e.g., "10m", "1h30m").
func getEnvDuration(key string, defaultVal time.Duration) time.Duration {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	// Try parsing as integer seconds first (simpler for env config)
	if secs, err := strconv.Atoi(val); err == nil {
		return time.Duration(secs) * time.Second
	}
	// Fall back to Go duration format (e.g., "10m", "1h30m")
	if d, err := time.ParseDuration(val); err == nil {
		return d
	}
	return defaultVal
}

// configuredTimeoutSeconds hold config-file timeout defaults installed by
// SetConfiguredTimeouts at startup. Zero means "not configured" and falls back
// to the built-in default.
var (
	configuredTimeoutSeconds               atomic.Int64
	configuredResponseHeaderTimeoutSeconds atomic.Int64
)

// SetConfiguredTimeouts installs the config-file (`http:` block) timeout
// defaults, in seconds. App startup calls this once before providers are
// constructed. The HTTP_TIMEOUT / HTTP_RESPONSE_HEADER_TIMEOUT env vars still
// take precedence, matching the project-wide env-over-YAML convention.
// Non-positive values clear the configured default.
func SetConfiguredTimeouts(timeoutSeconds, responseHeaderTimeoutSeconds int) {
	configuredTimeoutSeconds.Store(int64(max(timeoutSeconds, 0)))
	configuredResponseHeaderTimeoutSeconds.Store(int64(max(responseHeaderTimeoutSeconds, 0)))
}

func configuredOrDefault(configured *atomic.Int64, fallback time.Duration) time.Duration {
	if secs := configured.Load(); secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return fallback
}

// DefaultConfig returns a ClientConfig with sensible defaults for API clients.
// Timeout values match OpenAI/Anthropic SDK defaults (10 minutes).
// Precedence for the two request timeouts, highest first:
//   - HTTP_TIMEOUT / HTTP_RESPONSE_HEADER_TIMEOUT env vars (seconds, or Go
//     duration format)
//   - the config-file `http:` block installed via SetConfiguredTimeouts
//   - the built-in 600s default
func DefaultConfig() ClientConfig {
	defaultLongTimeout := 600 * time.Second
	return ClientConfig{
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   100,
		IdleConnTimeout:       90 * time.Second,
		Timeout:               getEnvDuration("HTTP_TIMEOUT", configuredOrDefault(&configuredTimeoutSeconds, defaultLongTimeout)),
		DialTimeout:           30 * time.Second,
		KeepAlive:             30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: getEnvDuration("HTTP_RESPONSE_HEADER_TIMEOUT", configuredOrDefault(&configuredResponseHeaderTimeoutSeconds, defaultLongTimeout)),
	}
}

// NewHTTPClient creates a new HTTP client with the provided configuration.
// If config is nil, DefaultConfig() is used.
func NewHTTPClient(config *ClientConfig) *http.Client {
	if config == nil {
		cfg := DefaultConfig()
		config = &cfg
	}

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   config.DialTimeout,
			KeepAlive: config.KeepAlive,
		}).DialContext,
		MaxIdleConns:          config.MaxIdleConns,
		MaxIdleConnsPerHost:   config.MaxIdleConnsPerHost,
		IdleConnTimeout:       config.IdleConnTimeout,
		TLSHandshakeTimeout:   config.TLSHandshakeTimeout,
		ResponseHeaderTimeout: config.ResponseHeaderTimeout,
		ForceAttemptHTTP2:     true,
		ExpectContinueTimeout: 1 * time.Second,
	}

	return &http.Client{
		Transport: transport,
		Timeout:   config.Timeout,
	}
}

// NewDefaultHTTPClient creates a new HTTP client with default configuration.
// This is a convenience function equivalent to NewHTTPClient(nil).
func NewDefaultHTTPClient() *http.Client {
	return NewHTTPClient(nil)
}
