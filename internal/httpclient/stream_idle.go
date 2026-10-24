package httpclient

import (
	"sync/atomic"
	"time"
)

// DefaultStreamIdleTimeout is how long an upstream stream may stay silent,
// once its first bytes arrived, before it is treated as stalled. Healthy
// streams send tokens or keep-alive events far more often than this.
const DefaultStreamIdleTimeout = 300 * time.Second

var (
	streamIdleTimeoutConfigured        atomic.Bool
	configuredStreamIdleTimeoutSeconds atomic.Int64
)

// SetConfiguredStreamIdleTimeout installs the config-file (`http:` block)
// stream idle timeout in seconds; 0 disables it. App startup calls this once
// before providers are constructed. HTTP_STREAM_IDLE_TIMEOUT still takes
// precedence.
func SetConfiguredStreamIdleTimeout(seconds int) {
	configuredStreamIdleTimeoutSeconds.Store(int64(max(seconds, 0)))
	streamIdleTimeoutConfigured.Store(true)
}

// StreamIdleTimeout resolves the stream idle timeout, highest precedence
// first: the HTTP_STREAM_IDLE_TIMEOUT env var (seconds or Go duration), the
// config-file value, then DefaultStreamIdleTimeout. Zero means disabled.
func StreamIdleTimeout() time.Duration {
	fallback := DefaultStreamIdleTimeout
	if streamIdleTimeoutConfigured.Load() {
		fallback = time.Duration(configuredStreamIdleTimeoutSeconds.Load()) * time.Second
	}
	return max(getEnvDuration("HTTP_STREAM_IDLE_TIMEOUT", fallback), 0)
}
