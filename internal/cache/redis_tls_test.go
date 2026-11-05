package cache

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/egress"
)

func TestRedisDialerPreservesTLSAndEgressHook(t *testing.T) {
	server := httptest.NewTLSServer(nil)
	t.Cleanup(server.Close)
	address := strings.TrimPrefix(server.URL, "https://")
	opts, err := redis.ParseURL("rediss://" + address)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	opts.TLSConfig.RootCAs = roots

	calls := 0
	hook, err := egress.Install(func(ctx context.Context, network, addr string) (net.Conn, error) {
		calls++
		assert.Equal(t, address, addr)
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	})
	require.NoError(t, err)
	t.Cleanup(hook.Uninstall)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	conn, err := redisDialer(opts.TLSConfig)(ctx, "tcp", address)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	tlsConn, ok := conn.(*tls.Conn)
	require.True(t, ok, "rediss must not return a plaintext connection")
	assert.True(t, tlsConn.ConnectionState().HandshakeComplete)
	assert.NotEmpty(t, tlsConn.ConnectionState().VerifiedChains)
	assert.Equal(t, 1, calls)
}

func TestRedisDialerRejectsUntrustedTLS(t *testing.T) {
	server := httptest.NewTLSServer(nil)
	t.Cleanup(server.Close)
	address := strings.TrimPrefix(server.URL, "https://")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	conn, err := redisDialer(&tls.Config{RootCAs: x509.NewCertPool()})(ctx, "tcp", address)
	require.Error(t, err)
	assert.Nil(t, conn)
}

func TestRedisDialerPreservesPlaintextAndHookDenial(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	denied := errors.New("egress denied")
	hook, err := egress.Install(func(_ context.Context, _, address string) (net.Conn, error) {
		if address == "denied:6379" {
			return nil, denied
		}
		return client, nil
	})
	require.NoError(t, err)
	t.Cleanup(hook.Uninstall)
	conn, err := redisDialer(nil)(context.Background(), "tcp", "allowed:6379")
	require.NoError(t, err)
	assert.Same(t, client, conn)
	conn, err = redisDialer(&tls.Config{})(context.Background(), "tcp", "denied:6379")
	require.ErrorIs(t, err, denied)
	assert.Nil(t, conn)
}
