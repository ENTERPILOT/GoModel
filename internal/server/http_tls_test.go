package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/servertls"
	"github.com/enterpilot/gomodel/internal/tlstest"
)

// The gateway serves a pre-bound listener in production, which Echo never
// wraps itself. Over TLS it must still negotiate HTTP/2 and report the socket
// peer as the client address, with no proxy in between to hide it.
func TestStartWithListenerServesTLS(t *testing.T) {
	ca := tlstest.NewCA(t, "test CA")
	certFile, keyFile := ca.Issue(t, "gateway").Write(t, t.TempDir())
	source, err := servertls.New(servertls.Settings{CertFile: certFile, KeyFile: keyFile})
	require.NoError(t, err)

	srv := New(&mockProvider{}, &Config{TLSConfig: source.ServerConfig()})
	srv.echo.GET("/debug/ip", func(c *echo.Context) error {
		return c.String(http.StatusOK, c.RealIP()+" "+c.Request().Proto+" "+c.Scheme())
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.StartWithListener(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done)
	})
	addr := listener.Addr().String()

	transport := &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: ca.Pool, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
	}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	resp, err := client.Get("https://" + addr + "/debug/ip")
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, resp.Body.Close())
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "127.0.0.1 HTTP/2.0 https", string(body))

	plain := &http.Client{Timeout: 5 * time.Second}
	resp, err = plain.Get("http://" + addr + "/health")
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "plain HTTP must not be served on a TLS listener")
}

// Under mTLS only the health probes answer without a verified client
// certificate, so the kubelet's HTTPS probes and `gomodel --health` work
// while everything else stays closed.
func TestClientCertificateRequired(t *testing.T) {
	ca := tlstest.NewCA(t, "test CA")
	clientCA := tlstest.NewCA(t, "client CA")
	dir := t.TempDir()
	certFile, keyFile := ca.Issue(t, "gateway").Write(t, dir)
	caFile := filepath.Join(dir, "client-ca.pem")
	tlstest.WriteFile(t, caFile, clientCA.PEM)
	source, err := servertls.New(servertls.Settings{CertFile: certFile, KeyFile: keyFile, ClientCAFile: caFile})
	require.NoError(t, err)

	srv := New(&mockProvider{}, &Config{
		BasePath:                   "/gw",
		TLSConfig:                  source.ServerConfig(),
		ClientCertificatesRequired: source.MutualTLS(),
	})
	srv.echo.GET("/private", func(c *echo.Context) error { return c.String(http.StatusOK, "ok") })
	// An extension catch-all must not answer an encoded health path.
	srv.echo.GET("/*", func(c *echo.Context) error { return c.String(http.StatusOK, "catch-all") })
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.StartWithListener(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done)
	})
	base := "https://" + listener.Addr().String() + "/gw"

	leaf := clientCA.Issue(t, "client", x509.ExtKeyUsageClientAuth)
	pair, err := tls.X509KeyPair(leaf.CertPEM, leaf.KeyPEM)
	require.NoError(t, err)
	status := func(certs []tls.Certificate, path string) int {
		transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: ca.Pool, Certificates: certs, MinVersion: tls.VersionTLS12}}
		defer transport.CloseIdleConnections()
		resp, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Get(base + path)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		return resp.StatusCode
	}

	assert.Equal(t, http.StatusOK, status(nil, "/health"))
	assert.NotEqual(t, http.StatusUnauthorized, status(nil, "/health/ready"))
	assert.Equal(t, http.StatusUnauthorized, status(nil, "/private"))
	assert.Equal(t, http.StatusUnauthorized, status(nil, "/health/../private"))
	assert.Equal(t, http.StatusUnauthorized, status(nil, "/health/"))
	assert.Equal(t, http.StatusUnauthorized, status(nil, "/%68ealth"))
	assert.Equal(t, http.StatusUnauthorized, status(nil, "/health%2Fready"))
	assert.Equal(t, http.StatusOK, status([]tls.Certificate{pair}, "/private"))
}
