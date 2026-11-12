package httpclient

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A provider connection that dies silently (a NAT or load balancer dropping
// it without a FIN or RST) must not keep every request multiplexed on it
// waiting for TCP keepalive or ResponseHeaderTimeout: the HTTP/2 ping health
// check closes it so the request fails fast and the next one redials.
func TestHTTP2SilentConnectionFailsFast(t *testing.T) {
	resetTLS(t)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	proxy := startSilenceableProxy(t, srv.Listener.Addr().String())

	cfg := DefaultConfig()
	cfg.HTTP2SendPingTimeout = 200 * time.Millisecond
	cfg.HTTP2PingTimeout = 200 * time.Millisecond
	client := NewHTTPClient(&cfg)

	// Trust the test server only after the client exists, so the request
	// below runs on the transport rebuilt for the new trust configuration.
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(caFile, pemEncode(t, srv.Certificate().Raw), 0o600))
	require.NoError(t, SetConfiguredTLS(TLSSettings{CAFile: caFile}))

	url := "https://" + proxy.addr()
	resp, err := client.Get(url)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, 2, resp.ProtoMajor, "test server should negotiate HTTP/2")

	transport := underlyingTransport(t, client)
	require.NotNil(t, transport.HTTP2, "rebuilt transport lost its HTTP/2 health check")
	assert.Equal(t, 200*time.Millisecond, transport.HTTP2.SendPingTimeout)
	assert.Equal(t, 200*time.Millisecond, transport.HTTP2.PingTimeout)

	proxy.silence()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	start := time.Now()
	resp, err = client.Do(req)
	elapsed := time.Since(start)
	if resp != nil {
		require.NoError(t, resp.Body.Close())
	}
	require.Error(t, err)
	require.NotErrorIs(t, err, context.DeadlineExceeded, "request hung on the dead connection")
	assert.Less(t, elapsed, 5*time.Second)
}

// silenceableProxy forwards TCP to a backend until silence is called; from
// then on it swallows every byte in both directions without closing the
// connections it already carries, the way a dead NAT mapping behaves, and
// closes any new connection immediately.
type silenceableProxy struct {
	ln      net.Listener
	backend string
	silent  atomic.Bool

	mu    sync.Mutex
	conns []net.Conn
}

func startSilenceableProxy(t *testing.T, backend string) *silenceableProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	p := &silenceableProxy{ln: ln, backend: backend}
	go p.serve()
	t.Cleanup(p.close)
	return p
}

func (p *silenceableProxy) addr() string { return p.ln.Addr().String() }

func (p *silenceableProxy) silence() { p.silent.Store(true) }

func (p *silenceableProxy) serve() {
	for {
		client, err := p.ln.Accept()
		if err != nil {
			return
		}
		if p.silent.Load() {
			_ = client.Close()
			continue
		}
		upstream, err := net.Dial("tcp", p.backend)
		if err != nil {
			_ = client.Close()
			continue
		}
		p.mu.Lock()
		p.conns = append(p.conns, client, upstream)
		p.mu.Unlock()
		go p.pipe(upstream, client)
		go p.pipe(client, upstream)
	}
}

func (p *silenceableProxy) pipe(dst, src net.Conn) {
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 && !p.silent.Load() {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			_ = dst.Close()
			return
		}
	}
}

func (p *silenceableProxy) close() {
	_ = p.ln.Close()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.conns {
		_ = c.Close()
	}
}
