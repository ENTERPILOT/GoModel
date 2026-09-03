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

	// TLS configures client-side trust for every outbound HTTPS connection.
	TLS HTTPTLSConfig `yaml:"tls"`
}

// HTTPTLSConfig configures client TLS trust for outbound connections: providers,
// the model catalog, the update check, MCP upstreams, vector stores, and
// embedding endpoints. All fields are optional; the zero value trusts the
// operating system certificate store.
type HTTPTLSConfig struct {
	// CAFile is a PEM bundle of additional root certificates, appended to the
	// system store. Set it for a private CA behind a TLS-intercepting proxy or
	// in front of local model servers.
	CAFile string `yaml:"ca_file" env:"HTTP_TLS_CA_FILE"`

	// ClientCertFile and ClientKeyFile present a client certificate (mTLS) to
	// upstreams that request one. Both must be set together.
	ClientCertFile string `yaml:"client_cert_file" env:"HTTP_TLS_CLIENT_CERT_FILE"`
	ClientKeyFile  string `yaml:"client_key_file" env:"HTTP_TLS_CLIENT_KEY_FILE"`

	// InsecureSkipVerify disables certificate verification for every outbound
	// connection. Lab use only; the gateway logs a warning at startup.
	InsecureSkipVerify bool `yaml:"insecure_skip_verify" env:"HTTP_TLS_INSECURE_SKIP_VERIFY"`
}
