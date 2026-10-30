package httpclient

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testPKI is a private CA with one server and one client certificate it
// signed, written to PEM files the way an operator would deploy them.
type testPKI struct {
	caPool                    *x509.CertPool
	server                    tls.Certificate
	caFile, certFile, keyFile string
}

func newTestPKI(t *testing.T) testPKI {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "gomodel test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	caCert, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)

	issue := func(serial int64, usage x509.ExtKeyUsage) ([]byte, *ecdsa.PrivateKey) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(serial),
			Subject:      pkix.Name{CommonName: "gomodel test leaf"},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{usage},
			IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
		require.NoError(t, err)
		return der, key
	}
	keyPEM := func(key *ecdsa.PrivateKey) []byte {
		der, err := x509.MarshalECPrivateKey(key)
		require.NoError(t, err)
		return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	}

	serverDER, serverKey := issue(2, x509.ExtKeyUsageServerAuth)
	server, err := tls.X509KeyPair(pemEncode(t, serverDER), keyPEM(serverKey))
	require.NoError(t, err)
	clientDER, clientKey := issue(3, x509.ExtKeyUsageClientAuth)

	dir := t.TempDir()
	pki := testPKI{
		caPool:   x509.NewCertPool(),
		server:   server,
		caFile:   filepath.Join(dir, "ca.pem"),
		certFile: filepath.Join(dir, "client.crt"),
		keyFile:  filepath.Join(dir, "client.key"),
	}
	pki.caPool.AddCert(caCert)
	require.NoError(t, os.WriteFile(pki.caFile, pemEncode(t, caDER), 0o600))
	require.NoError(t, os.WriteFile(pki.certFile, pemEncode(t, clientDER), 0o600))
	require.NoError(t, os.WriteFile(pki.keyFile, keyPEM(clientKey), 0o600))
	return pki
}

// mTLSServer requires a client certificate signed by the test CA.
func mTLSServer(t *testing.T, pki testPKI) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.PeerCertificates) == 0 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{pki.server},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pki.caPool,
		MinVersion:   tls.VersionTLS12,
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func TestSetConfiguredTLS_ClientCertificateCompletesMTLS(t *testing.T) {
	resetTLS(t)
	pki := newTestPKI(t)
	srv := mTLSServer(t, pki)

	// Trusting the CA alone is not enough: the server demands a certificate.
	require.NoError(t, SetConfiguredTLS(TLSSettings{CAFile: pki.caFile}))
	resp, err := NewClientWithTimeout(5 * time.Second).Get(srv.URL)
	if resp != nil {
		require.NoError(t, resp.Body.Close())
	}
	require.Error(t, err, "expected the handshake to fail without a client certificate")

	require.NoError(t, SetConfiguredTLS(TLSSettings{
		CAFile:         pki.caFile,
		ClientCertFile: pki.certFile,
		ClientKeyFile:  pki.keyFile,
	}))
	resp, err = NewClientWithTimeout(5 * time.Second).Get(srv.URL)
	require.NoError(t, err, "expected the mTLS handshake to succeed")
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

// A reload re-reads the files but must keep pooled connections when they
// still hold the same bytes, and swap the transport when they do not.
func TestSetConfiguredTLS_UnchangedMaterialKeepsTransport(t *testing.T) {
	resetTLS(t)
	pki := newTestPKI(t)
	settings := TLSSettings{CAFile: pki.caFile, ClientCertFile: pki.certFile, ClientKeyFile: pki.keyFile}
	require.NoError(t, SetConfiguredTLS(settings))
	client := NewDefaultHTTPClient()
	before := underlyingTransport(t, client)

	require.NoError(t, SetConfiguredTLS(settings))
	assert.Same(t, before, underlyingTransport(t, client), "identical TLS material must not rebuild the transport")

	require.NoError(t, SetConfiguredTLS(TLSSettings{}))
	defaults := underlyingTransport(t, client)
	assert.NotSame(t, before, defaults, "switching to system defaults must rebuild the transport")
	require.NoError(t, SetConfiguredTLS(TLSSettings{}))
	assert.Same(t, defaults, underlyingTransport(t, client), "repeated system defaults must not rebuild the transport")

	require.NoError(t, SetConfiguredTLS(settings))
	reloaded := underlyingTransport(t, client)
	// Same paths, new CA bundle contents: the change must take effect.
	rotated := newTestPKI(t)
	data, err := os.ReadFile(rotated.caFile)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(pki.caFile, data, 0o600))
	require.NoError(t, SetConfiguredTLS(settings))
	assert.NotSame(t, reloaded, underlyingTransport(t, client), "changed file contents must rebuild the transport")
}
