package config

// HTTPConfig holds HTTP client configuration for upstream API requests. App
// startup installs these values into internal/httpclient before providers are
// constructed; the HTTP_TIMEOUT and HTTP_RESPONSE_HEADER_TIMEOUT env vars take
// precedence over the YAML values.
type HTTPConfig struct {
	// Timeout is the overall HTTP request timeout in seconds (default: 600)
	Timeout int `yaml:"timeout" env:"HTTP_TIMEOUT"`

	// ResponseHeaderTimeout is the time to wait for response headers in seconds (default: 600)
	ResponseHeaderTimeout int `yaml:"response_header_timeout" env:"HTTP_RESPONSE_HEADER_TIMEOUT"`

	// StreamIdleTimeout is the longest silence, in seconds, a streaming
	// response may keep once its first bytes arrived before GoModel gives up
	// on it (default: 300). 0 disables it. The HTTP_STREAM_IDLE_TIMEOUT env var
	// is read by internal/httpclient instead of the generic env overlay, so it
	// accepts Go durations such as "2m" as well as seconds.
	StreamIdleTimeout int `yaml:"stream_idle_timeout"`
}
