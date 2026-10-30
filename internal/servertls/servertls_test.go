package servertls

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/tlstest"
)

// newSource loads settings and checks the files on every handshake, so a
// test sees a rotation without waiting out DefaultCheckInterval.
func newSource(t *testing.T, settings Settings) *Source {
	t.Helper()
	src, err := New(settings)
	require.NoError(t, err)
	src.interval = 0
	src.nextCheck.Store(0)
	return src
}

// serve runs an HTTPS server on loopback that answers with the request's
// protocol, and returns its address.
func serve(t *testing.T, src *Source) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, r.Proto)
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = srv.Serve(tls.NewListener(ln, src.ServerConfig())) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

// servedLeaf opens a fresh connection and returns the certificate served on it.
func servedLeaf(t *testing.T, addr string, roots *x509.CertPool) *x509.Certificate {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	return conn.ConnectionState().PeerCertificates[0]
}

// get makes one HTTPS request on a fresh connection and returns the body.
func get(addr string, cfg *tls.Config) (string, error) {
	transport := &http.Transport{TLSClientConfig: cfg, ForceAttemptHTTP2: true, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	resp, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Get("https://" + addr + "/")
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	return string(body), err
}

func TestServesHTTPSWithHTTP2(t *testing.T) {
	ca := tlstest.NewCA(t, "test CA")
	certFile, keyFile := ca.Issue(t, "gateway").Write(t, t.TempDir())
	addr := serve(t, newSource(t, Settings{CertFile: certFile, KeyFile: keyFile}))

	proto, err := get(addr, &tls.Config{RootCAs: ca.Pool, MinVersion: tls.VersionTLS12})
	require.NoError(t, err)
	assert.Equal(t, "HTTP/2.0", proto)

	_, err = get(addr, &tls.Config{RootCAs: ca.Pool, MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS11})
	require.Error(t, err, "TLS below 1.2 must be refused")
}

func TestNewRejectsUnusableFiles(t *testing.T) {
	ca := tlstest.NewCA(t, "test CA")
	dir := t.TempDir()
	certFile, keyFile := ca.Issue(t, "gateway").Write(t, dir)
	_, otherKey := ca.Issue(t, "other").Write(t, t.TempDir())
	garbage := filepath.Join(dir, "garbage.pem")
	require.NoError(t, os.WriteFile(garbage, []byte("not a certificate"), 0o600))
	missing := filepath.Join(dir, "missing.pem")
	noDashes := filepath.Join(dir, "no-dashes-ca.pem")
	require.NoError(t, os.WriteFile(noDashes, append(append([]byte{}, ca.PEM...), "-----BEGIN CERTIFICATE\nAAAA\n-----END CERTIFICATE-----\n"...), 0o600))
	badFraming := filepath.Join(dir, "bad-framing-ca.pem")
	require.NoError(t, os.WriteFile(badFraming, append([]byte("-----BEGIN CERTIFICATE-----\n!!!\n-----END CERTIFICATE-----\n"), ca.PEM...), 0o600))
	malformedCA := filepath.Join(dir, "malformed-ca.pem")
	require.NoError(t, os.WriteFile(malformedCA, append(append([]byte{}, ca.PEM...), "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"...), 0o600))

	tests := []struct {
		name     string
		settings Settings
		wantErr  string
	}{
		{"missing certificate", Settings{CertFile: missing, KeyFile: keyFile}, "missing.pem"},
		{"missing key", Settings{CertFile: certFile, KeyFile: missing}, "missing.pem"},
		{"certificate is not PEM", Settings{CertFile: garbage, KeyFile: keyFile}, "load certificate"},
		{"key does not match", Settings{CertFile: certFile, KeyFile: otherKey}, "load certificate"},
		{"missing client CA", Settings{CertFile: certFile, KeyFile: keyFile, ClientCAFile: missing}, "missing.pem"},
		{"client CA is not PEM", Settings{CertFile: certFile, KeyFile: keyFile, ClientCAFile: garbage}, "no PEM certificates"},
		{"client CA bundle drops a CA", Settings{CertFile: certFile, KeyFile: keyFile, ClientCAFile: malformedCA}, "certificate 2"},
		{"client CA block with bad base64", Settings{CertFile: certFile, KeyFile: keyFile, ClientCAFile: badFraming}, "1 of 2 PEM blocks are malformed"},
		{"client CA BEGIN line without dashes", Settings{CertFile: certFile, KeyFile: keyFile, ClientCAFile: noDashes}, "1 of 2 PEM blocks are malformed"},
		{"half configured", Settings{CertFile: certFile}, "both a certificate and a key"},
		{"certificate is a directory", Settings{CertFile: dir, KeyFile: keyFile}, "not a regular file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.settings)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// PEM contents passed where a path belongs, as with a ${file:...} reference
// that resolves to the key itself, must never be echoed into the error, which
// is logged.
func TestNewRejectsPEMContentsAsPath(t *testing.T) {
	ca := tlstest.NewCA(t, "test CA")
	leaf := ca.Issue(t, "gateway")
	certFile, keyFile := leaf.Write(t, t.TempDir())
	key := string(leaf.KeyPEM)

	tests := []struct {
		name     string
		settings Settings
		field    string
	}{
		{"certificate", Settings{CertFile: string(leaf.CertPEM), KeyFile: keyFile}, "server.tls_cert_file"},
		{"key", Settings{CertFile: certFile, KeyFile: key}, "server.tls_key_file"},
		{"client CA", Settings{CertFile: certFile, KeyFile: keyFile, ClientCAFile: string(ca.PEM)}, "server.tls_client_ca_file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.settings)
			require.EqualError(t, err, tt.field+" expects a file path, not PEM contents")
		})
	}
}

func TestReloadsRotatedCertificate(t *testing.T) {
	ca := tlstest.NewCA(t, "test CA")
	dir := t.TempDir()
	first := ca.Issue(t, "first")
	certFile, keyFile := first.Write(t, dir)
	addr := serve(t, newSource(t, Settings{CertFile: certFile, KeyFile: keyFile}))
	require.Equal(t, first.Cert.SerialNumber, servedLeaf(t, addr, ca.Pool).SerialNumber)

	second := ca.Issue(t, "second")
	second.Write(t, dir)
	assert.Equal(t, second.Cert.SerialNumber, servedLeaf(t, addr, ca.Pool).SerialNumber)
}

// Kubernetes updates a mounted Secret by pointing the ..data symlink at a new
// directory; the file names the gateway was given never change.
func TestReloadsAfterKubernetesSymlinkSwap(t *testing.T) {
	ca := tlstest.NewCA(t, "test CA")
	mount := t.TempDir()
	first, second := ca.Issue(t, "first"), ca.Issue(t, "second")
	for name, leaf := range map[string]tlstest.Leaf{"..v1": first, "..v2": second} {
		require.NoError(t, os.Mkdir(filepath.Join(mount, name), 0o700))
		leaf.Write(t, filepath.Join(mount, name))
	}
	data := filepath.Join(mount, "..data")
	require.NoError(t, os.Symlink("..v1", data))
	for _, name := range []string{"tls.crt", "tls.key"} {
		require.NoError(t, os.Symlink(filepath.Join("..data", name), filepath.Join(mount, name)))
	}
	addr := serve(t, newSource(t, Settings{
		CertFile: filepath.Join(mount, "tls.crt"),
		KeyFile:  filepath.Join(mount, "tls.key"),
	}))
	require.Equal(t, first.Cert.SerialNumber, servedLeaf(t, addr, ca.Pool).SerialNumber)

	swap := filepath.Join(mount, "..data_tmp")
	require.NoError(t, os.Symlink("..v2", swap))
	require.NoError(t, os.Rename(swap, data))
	assert.Equal(t, second.Cert.SerialNumber, servedLeaf(t, addr, ca.Pool).SerialNumber)
}

func TestFailedReloadKeepsLastGoodCertificate(t *testing.T) {
	ca := tlstest.NewCA(t, "test CA")
	dir := t.TempDir()
	first := ca.Issue(t, "first")
	certFile, keyFile := first.Write(t, dir)
	addr := serve(t, newSource(t, Settings{CertFile: certFile, KeyFile: keyFile}))

	// The new certificate landed but its key has not yet: a mismatched pair.
	second := ca.Issue(t, "second")
	tlstest.WriteFile(t, certFile, second.CertPEM)
	assert.Equal(t, first.Cert.SerialNumber, servedLeaf(t, addr, ca.Pool).SerialNumber)

	// A file truncated mid-write, then one removed outright.
	tlstest.WriteFile(t, certFile, second.CertPEM[:len(second.CertPEM)/2])
	assert.Equal(t, first.Cert.SerialNumber, servedLeaf(t, addr, ca.Pool).SerialNumber)
	require.NoError(t, os.Remove(keyFile))
	assert.Equal(t, first.Cert.SerialNumber, servedLeaf(t, addr, ca.Pool).SerialNumber)

	// The pair completes, and the next handshake picks it up.
	second.Write(t, dir)
	assert.Equal(t, second.Cert.SerialNumber, servedLeaf(t, addr, ca.Pool).SerialNumber)
}

// A fix that leaves the files' stat unchanged, such as restored permissions,
// must still be picked up: a failed load is retried on the next check.
func TestRetriesFailedReloadWithoutFileChange(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads files regardless of their permissions")
	}
	ca := tlstest.NewCA(t, "test CA")
	dir := t.TempDir()
	first := ca.Issue(t, "first")
	certFile, keyFile := first.Write(t, dir)
	addr := serve(t, newSource(t, Settings{CertFile: certFile, KeyFile: keyFile}))

	second := ca.Issue(t, "second")
	second.Write(t, dir)
	require.NoError(t, os.Chmod(keyFile, 0))
	assert.Equal(t, first.Cert.SerialNumber, servedLeaf(t, addr, ca.Pool).SerialNumber)

	require.NoError(t, os.Chmod(keyFile, 0o600))
	assert.Equal(t, second.Cert.SerialNumber, servedLeaf(t, addr, ca.Pool).SerialNumber)
}

// Between checks a handshake reuses the loaded certificate without touching
// the file system.
func TestChecksFilesAtMostOncePerInterval(t *testing.T) {
	ca := tlstest.NewCA(t, "test CA")
	dir := t.TempDir()
	first := ca.Issue(t, "first")
	certFile, keyFile := first.Write(t, dir)
	src, err := New(Settings{CertFile: certFile, KeyFile: keyFile})
	require.NoError(t, err)
	addr := serve(t, src)

	ca.Issue(t, "second").Write(t, dir)
	assert.Equal(t, first.Cert.SerialNumber, servedLeaf(t, addr, ca.Pool).SerialNumber)
}

func TestMutualTLS(t *testing.T) {
	ca := tlstest.NewCA(t, "test CA")
	clientCA := tlstest.NewCA(t, "client CA")
	dir := t.TempDir()
	certFile, keyFile := ca.Issue(t, "gateway", x509.ExtKeyUsageServerAuth).Write(t, dir)
	caFile := filepath.Join(dir, "client-ca.pem")
	tlstest.WriteFile(t, caFile, clientCA.PEM)
	src := newSource(t, Settings{CertFile: certFile, KeyFile: keyFile, ClientCAFile: caFile})
	require.True(t, src.MutualTLS())
	addr := serve(t, src)

	clientCert := func(issuer *tlstest.CA) []tls.Certificate {
		leaf := issuer.Issue(t, "client", x509.ExtKeyUsageClientAuth)
		pair, err := tls.X509KeyPair(leaf.CertPEM, leaf.KeyPEM)
		require.NoError(t, err)
		return []tls.Certificate{pair}
	}
	// GetClientCertificate sends the certificate even when its issuer is not
	// among the CAs the server asks for; Go would otherwise withhold it.
	request := func(certs []tls.Certificate) error {
		cfg := &tls.Config{RootCAs: ca.Pool, MinVersion: tls.VersionTLS12}
		if len(certs) > 0 {
			cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &certs[0], nil }
		}
		_, err := get(addr, cfg)
		return err
	}

	require.NoError(t, request(clientCert(clientCA)), "a client the CA signed must be accepted")
	require.NoError(t, request(nil), "a client without a certificate is left to the HTTP layer")
	require.Error(t, request(clientCert(ca)), "a client another CA signed must be rejected")

	// The client CA reloads too.
	rotated := tlstest.NewCA(t, "rotated client CA")
	tlstest.WriteFile(t, caFile, rotated.PEM)
	require.Error(t, request(clientCert(clientCA)), "the replaced CA must no longer be trusted")
	require.NoError(t, request(clientCert(rotated)))
}

// A marker quoted inside a comment line is not a block, as for pem.Decode.
func TestClientCABundleIgnoresQuotedMarker(t *testing.T) {
	ca := tlstest.NewCA(t, "test CA")
	clientCA := tlstest.NewCA(t, "client CA")
	dir := t.TempDir()
	certFile, keyFile := ca.Issue(t, "gateway").Write(t, dir)
	caFile := filepath.Join(dir, "client-ca.pem")
	tlstest.WriteFile(t, caFile, append([]byte("# paste each CA as -----BEGIN CERTIFICATE----- ... below\n"), clientCA.PEM...))

	src, err := New(Settings{CertFile: certFile, KeyFile: keyFile, ClientCAFile: caFile})
	require.NoError(t, err)
	assert.True(t, src.MutualTLS())
}

// The per-handshake path is one clock read and one atomic load between checks.
func BenchmarkConfigForClient(b *testing.B) {
	ca := tlstest.NewCA(b, "test CA")
	certFile, keyFile := ca.Issue(b, "gateway").Write(b, b.TempDir())
	src, err := New(Settings{CertFile: certFile, KeyFile: keyFile})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := src.configForClient(nil); err != nil {
			b.Fatal(err)
		}
	}
}
