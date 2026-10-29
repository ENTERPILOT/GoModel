package httpclient

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"

	"github.com/enterpilot/gomodel/internal/pemfile"
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

// trustState is one installed trust configuration. Each change installs a
// fresh value, so the pointer doubles as a generation marker: a transport
// bound to a different pointer than the one currently installed is stale and
// rebuilds itself on its next request (see dynamicTransport). A nil state
// means system defaults.
type trustState struct {
	cfg *tls.Config
	// fingerprint identifies the settings and the file contents cfg was built
	// from, so re-installing identical material keeps the current state and
	// every transport's connection pool.
	fingerprint [sha256.Size]byte
}

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
//
// Installing settings whose files hold the same bytes as the installed ones
// is a no-op, so a reload that changes nothing here keeps every pooled
// upstream connection.
func SetConfiguredTLS(settings TLSSettings) error {
	state, err := buildTrustState(settings)
	if err != nil {
		return err
	}
	if sameTrust(trust.Load(), state) {
		return nil
	}
	trust.Store(state)
	return nil
}

func sameTrust(a, b *trustState) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.fingerprint == b.fingerprint
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

// buildTrustState reads the referenced files once and builds the TLS
// configuration from those bytes, fingerprinting the same bytes. It returns
// nil for system defaults.
func buildTrustState(settings TLSSettings) (*trustState, error) {
	caFile := strings.TrimSpace(settings.CAFile)
	certFile := strings.TrimSpace(settings.ClientCertFile)
	keyFile := strings.TrimSpace(settings.ClientKeyFile)

	if caFile == "" && certFile == "" && keyFile == "" && !settings.InsecureSkipVerify {
		return nil, nil
	}
	if (certFile == "") != (keyFile == "") {
		return nil, fmt.Errorf("http.tls: client_cert_file and client_key_file must be set together")
	}
	if err := errors.Join(
		pemfile.CheckPath("http.tls.ca_file", caFile),
		pemfile.CheckPath("http.tls.client_cert_file", certFile),
		pemfile.CheckPath("http.tls.client_key_file", keyFile),
	); err != nil {
		return nil, err
	}

	hash := sha256.New()
	fmt.Fprintf(hash, "insecure=%t\n", settings.InsecureSkipVerify)
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}

	if caFile != "" {
		caPEM, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("http.tls: read ca_file: %w", err)
		}
		pool := systemCertPool()
		if err := pemfile.AppendCertificates(pool, caPEM); err != nil {
			return nil, fmt.Errorf("http.tls: ca_file %s: %w", caFile, err)
		}
		cfg.RootCAs = pool
		writeMaterial(hash, "ca", caFile, caPEM)
	}

	if certFile != "" {
		certPEM, err := os.ReadFile(certFile)
		if err != nil {
			return nil, fmt.Errorf("http.tls: read client_cert_file: %w", err)
		}
		keyPEM, err := os.ReadFile(keyFile)
		if err != nil {
			return nil, fmt.Errorf("http.tls: read client_key_file: %w", err)
		}
		cert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return nil, fmt.Errorf("http.tls: load client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
		writeMaterial(hash, "cert", certFile, certPEM)
		writeMaterial(hash, "key", keyFile, keyPEM)
	}

	//nolint:gosec // Operator opt-in for lab environments; startup logs a warning.
	cfg.InsecureSkipVerify = settings.InsecureSkipVerify
	state := &trustState{cfg: cfg}
	hash.Sum(state.fingerprint[:0])
	return state, nil
}

// writeMaterial adds one labeled file to the fingerprint, length-prefixed so
// adjacent fields cannot run into each other.
func writeMaterial(w io.Writer, label, path string, data []byte) {
	fmt.Fprintf(w, "%s %d:%s %d:", label, len(path), path, len(data))
	_, _ = w.Write(data)
}

// systemCertPool returns a copy of the operating system trust store, or an
// empty pool when it cannot be loaded. The fallback still trusts the
// configured CA, but nothing else, so it is logged: public upstreams would
// start failing verification.
func systemCertPool() *x509.CertPool {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		slog.Warn("could not load the system certificate pool; outbound TLS trusts only http.tls.ca_file", "error", err)
		return x509.NewCertPool()
	}
	return pool
}
