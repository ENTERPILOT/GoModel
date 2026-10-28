package server

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
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
