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
}
