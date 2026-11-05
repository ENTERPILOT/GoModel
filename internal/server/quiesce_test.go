package server

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/servertls"
	"github.com/enterpilot/gomodel/internal/tlstest"
)

// acceptRecorder reports every connection its server accepts, and when the
// server stops accepting: the quiesce deadline surfaces here as a timeout
// before the server sees it. Embedding the TCP listener keeps SetDeadline,
// which quiescing relies on.
type acceptRecorder struct {
	*net.TCPListener
	accepted chan struct{}
	parkOnce sync.Once
	parked   chan struct{}
}

func (l *acceptRecorder) Accept() (net.Conn, error) {
	conn, err := l.TCPListener.Accept()
	var netErr net.Error
	switch {
	case err == nil:
		l.accepted <- struct{}{}
	case errors.As(err, &netErr) && netErr.Timeout():
		l.parkOnce.Do(func() { close(l.parked) })
	}
	return conn, err
}

// generation serves one Server on its own duplicate of socket's descriptor,
// the way run hands each configuration generation the long-lived socket.
type generation struct {
	listener *acceptRecorder
	stop     context.CancelFunc
	done     chan error
	stopped  bool // awaitStopped received from done
}

func serveGeneration(t *testing.T, name string, socket *os.File, cfg *Config) *generation {
	t.Helper()
	ln, err := net.FileListener(socket)
	require.NoError(t, err)
	srv := New(nil, cfg)
	srv.echo.GET("/generation", func(c *echo.Context) error {
		return c.String(http.StatusOK, name)
	})
	g := &generation{
		listener: &acceptRecorder{
			TCPListener: ln.(*net.TCPListener),
			accepted:    make(chan struct{}, 16),
			parked:      make(chan struct{}),
		},
		done: make(chan error, 1),
	}
	ctx, cancel := context.WithCancel(context.Background())
	g.stop = cancel
	go func() { g.done <- srv.StartWithListener(ctx, g.listener) }()
	t.Cleanup(func() {
		cancel()
		if g.stopped {
			return
		}
		select {
		case <-g.done:
		case <-time.After(30 * time.Second):
			t.Error("server did not stop")
		}
	})
	return g
}

func (g *generation) awaitAccept(t *testing.T) {
	t.Helper()
	select {
	case <-g.accepted():
	case <-time.After(5 * time.Second):
		t.Fatal("the server never accepted the connection")
	}
}

func (g *generation) accepted() <-chan struct{} { return g.listener.accepted }

// dialUnaccepted waits for the stopping g to stop accepting, then opens a
// connection it must leave in the kernel's queue. A server that is not
// quiesced never stops accepting, so it fails here.
func (g *generation) dialUnaccepted(t *testing.T, addr string) net.Conn {
	t.Helper()
	select {
	case <-g.listener.parked:
	case <-time.After(5 * time.Second):
		t.Fatal("the stopping server never stopped accepting")
	}
	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	select {
	case <-g.accepted():
		t.Fatal("a stopping server accepted a new connection")
	case <-time.After(200 * time.Millisecond):
	}
	return conn
}

func (g *generation) awaitStopped(t *testing.T) {
	t.Helper()
	select {
	case err := <-g.done:
		g.stopped = true
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("server did not stop")
	}
}

func boundSocketFile(t *testing.T) (*os.File, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	file, err := ln.(*net.TCPListener).File()
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	t.Cleanup(func() { _ = file.Close() })
	return file, addr
}

func requestGeneration(t *testing.T, conn net.Conn) string {
	t.Helper()
	_, err := io.WriteString(conn, "GET /generation HTTP/1.1\r\nHost: gateway\r\n\r\n")
	require.NoError(t, err)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err, "the connection was closed without a response")
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, resp.Body.Close())
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return string(body)
}

// A reload stops one server and starts the next on the same socket. Before
// quiescing, a connection the outgoing server had accepted but not yet read a
// request from was closed without a response as soon as shutdown began:
// net/http drops a request it reads once Shutdown has started. Now the
// outgoing server answers what it accepted, and stops accepting so that what
// arrives meanwhile is answered by the next one.
func TestReloadHandoverAnswersEveryConnection(t *testing.T) {
	socket, addr := boundSocketFile(t)
	first := serveGeneration(t, "first", socket, &Config{})

	accepted, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = accepted.Close() })
	first.awaitAccept(t)

	first.stop()
	queued := first.dialUnaccepted(t, addr)

	assert.Equal(t, "first", requestGeneration(t, accepted))
	first.awaitStopped(t)

	second := serveGeneration(t, "second", socket, &Config{})
	assert.Equal(t, "second", requestGeneration(t, queued))
	second.stop()
	second.awaitStopped(t)
}

// Over TLS the server sees its tracked connection through the *tls.Conn that
// wraps it, so the handler has to unwrap it to settle it.
func TestQuiesceSettlesTLSConnections(t *testing.T) {
	ca := tlstest.NewCA(t, "test CA")
	certFile, keyFile := ca.Issue(t, "gateway").Write(t, t.TempDir())
	source, err := servertls.New(servertls.Settings{CertFile: certFile, KeyFile: keyFile})
	require.NoError(t, err)

	socket, addr := boundSocketFile(t)
	gen := serveGeneration(t, "tls", socket, &Config{TLSConfig: source.ServerConfig()})

	conn, err := tls.Dial("tcp", addr, &tls.Config{
		RootCAs:    ca.Pool,
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	gen.awaitAccept(t)

	// Settled, not waited out: the whole stop ends soon after the request,
	// far inside QuiesceTimeout. A connection the handler failed to settle
	// would hold it for the full timeout.
	start := time.Now()
	gen.stop()
	gen.dialUnaccepted(t, addr)
	assert.Equal(t, "tls", requestGeneration(t, conn))
	gen.awaitStopped(t)
	assert.Less(t, time.Since(start), QuiesceTimeout/2)
}

// quiesce is bounded: a client that connected and sent nothing does not hold
// the server past the timeout, and one that hangs up ends the wait at once.
func TestQuiesceIsBounded(t *testing.T) {
	setup := func(t *testing.T) (*quiescer, net.Listener, net.Conn) {
		t.Helper()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		q := newQuiescer()
		served := q.listen(ln)
		t.Cleanup(func() { _ = served.Close() })

		client, err := net.Dial("tcp", ln.Addr().String())
		require.NoError(t, err)
		t.Cleanup(func() { _ = client.Close() })
		conn, err := served.Accept()
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })

		// What Serve does: keep accepting until the deadline parks Accept.
		go func() { _, _ = served.Accept() }()
		return q, ln, conn
	}

	t.Run("silent client", func(t *testing.T) {
		q, ln, _ := setup(t)
		const timeout = 150 * time.Millisecond
		start := time.Now()
		q.quiesce(ln, timeout)
		elapsed := time.Since(start)
		assert.GreaterOrEqual(t, elapsed, timeout)
		assert.Less(t, elapsed, timeout+time.Second)
	})

	t.Run("client hangs up", func(t *testing.T) {
		q, ln, conn := setup(t)
		done := make(chan struct{})
		go func() {
			q.quiesce(ln, time.Minute)
			close(done)
		}()
		select {
		case <-q.parked:
		case <-time.After(10 * time.Second):
			t.Fatal("Accept never parked")
		}
		_ = conn.Close()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("quiesce kept waiting for a connection that closed")
		}
	})
}
