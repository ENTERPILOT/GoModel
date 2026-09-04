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

// trustState is one installed trust configuration. Each SetConfiguredTLS
// call installs a fresh value, so the pointer doubles as a generation
// marker: a transport bound to a different pointer than the one currently
// installed is stale and rebuilds itself on its next request (see
// dynamicTransport). A nil cfg means system defaults.
type trustState struct{ cfg *tls.Config }

func (s *trustState) tlsConfig() *tls.Config {
	if s == nil || s.cfg == nil {
		return nil
	}
	return s.cfg.Clone()
}

// trust holds the installed configuration. Nil means "system defaults",
// which is also what a zero TLSSettings produces.
var trust atomic.Pointer[trustState]

// SetConfiguredTLS builds and installs the client TLS configuration used by
// every HTTP client this package creates, including clients that already
// exist: transports pick the change up on their next request. App startup
// calls it before any provider constructs a transport. It returns an error
// when a referenced file is missing, unreadable, or holds no usable
// certificate, so a typo in the path fails startup instead of silently
// trusting nothing extra.
func SetConfiguredTLS(settings TLSSettings) error {
	cfg, err := buildTLSConfig(settings)
	if err != nil {
		return err
	}
	trust.Store(&trustState{cfg: cfg})
	return nil
}

// ConfiguredTLS returns a copy of the installed client TLS configuration, or
// nil when the gateway runs on system defaults. Callers that build their own
// transport (websocket dialers, SDK clients) should install it.
func ConfiguredTLS() *tls.Config {
	return trust.Load().tlsConfig()
}

// TLSSnapshot is an opaque handle to the installed configuration, taken with
// SnapshotTLS and given back to RestoreTLS. A reload that fails after
// installing replacement settings uses it to put the serving generation's
// trust back. Because every transport re-checks the installed configuration
// per request, the rollback also reaches clients that were created while the
// rejected settings were installed.
type TLSSnapshot struct{ state *trustState }

// SnapshotTLS captures the currently installed configuration.
func SnapshotTLS() TLSSnapshot { return TLSSnapshot{state: trust.Load()} }

// RestoreTLS reinstalls a configuration captured by SnapshotTLS. Transports
// bound to that same configuration keep their connection pools.
func RestoreTLS(s TLSSnapshot) { trust.Store(s.state) }

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
