package httpclient

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
)

// TLSSettings is the process-wide client TLS trust configuration for every
// outbound HTTPS connection the gateway opens: providers, the model catalog,
// the update check, MCP upstreams, vector stores, and embedding endpoints.
//
// Everything is optional. With the zero value, connections trust the operating
// system's certificate store, which is what most deployments want.
type TLSSettings struct {
	// CAFile is a PEM bundle appended to the system trust store. Use it for a
	// private CA that signs local model servers or a TLS-intercepting proxy.
	CAFile string
	// ClientCertFile and ClientKeyFile present a client certificate (mTLS) to
	// every upstream that asks for one. Both must be set together.
	ClientCertFile string
	ClientKeyFile  string
	// InsecureSkipVerify disables certificate verification entirely. It is
	// meant for a lab, never for production; the gateway logs a warning.
	InsecureSkipVerify bool
}

// configuredTLS holds the *tls.Config built by SetConfiguredTLS. Nil means
// "system defaults", which is also what a zero TLSSettings produces.
var configuredTLS atomic.Pointer[tls.Config]

// SetConfiguredTLS builds and installs the client TLS configuration used by
// every HTTP client this package creates. App startup calls it once, before
// any provider constructs a transport. It returns an error when a referenced
// file is missing, unreadable, or holds no usable certificate, so a typo in
// the path fails startup instead of silently trusting nothing extra.
func SetConfiguredTLS(settings TLSSettings) error {
	cfg, err := buildTLSConfig(settings)
	if err != nil {
		return err
	}
	configuredTLS.Store(cfg)
	return nil
}

// ConfiguredTLS returns a copy of the installed client TLS configuration, or
// nil when the gateway runs on system defaults. Callers that build their own
// transport (websocket dialers, SDK clients) should install it.
func ConfiguredTLS() *tls.Config {
	cfg := configuredTLS.Load()
	if cfg == nil {
		return nil
	}
	return cfg.Clone()
}

func buildTLSConfig(settings TLSSettings) (*tls.Config, error) {
	caFile := strings.TrimSpace(settings.CAFile)
	certFile := strings.TrimSpace(settings.ClientCertFile)
	keyFile := strings.TrimSpace(settings.ClientKeyFile)

	if caFile == "" && certFile == "" && keyFile == "" && !settings.InsecureSkipVerify {
		return nil, nil
	}
	if (certFile == "") != (keyFile == "") {
		return nil, fmt.Errorf("http.tls: client_cert_file and client_key_file must be set together")
	}

	cfg := &tls.Config{MinVersion: tls.VersionTLS12}

	if caFile != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("http.tls: read ca_file: %w", err)
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("http.tls: ca_file %s contains no PEM certificates", caFile)
		}
		cfg.RootCAs = pool
	}

	if certFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("http.tls: load client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}

	//nolint:gosec // Operator opt-in for lab environments; startup logs a warning.
	cfg.InsecureSkipVerify = settings.InsecureSkipVerify
	return cfg, nil
}
