package realtime_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/enterpilot/gomodel/internal/realtime"
)

func wsURL(httpURL string) string {
	return strings.Replace(httpURL, "http", "ws", 1)
}

// echoServer accepts a websocket and echoes every frame back unchanged.
func echoServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		c.SetReadLimit(realtime.MaxFrameBytes)
		defer c.Close(websocket.StatusNormalClosure, "")
		ctx := r.Context()
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			if err := c.Write(ctx, typ, data); err != nil {
				return
			}
		}
	}))
}

// proxyServer mounts realtime.Proxy in front of the given upstream ws URL and
// reports each Proxy return value on retc.
func proxyServer(t *testing.T, upstreamWS string, onServerFrame func([]byte), retc chan<- error) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := realtime.Proxy(w, r, realtime.Target{URL: upstreamWS}, onServerFrame)
		if retc != nil {
			retc <- err
		}
	}))
}

func dialClient(t *testing.T, proxyHTTP string) (*websocket.Conn, context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	c, _, err := websocket.Dial(ctx, wsURL(proxyHTTP), nil)
	if err != nil {
		cancel()
		t.Fatalf("client dial failed: %v", err)
	}
	c.SetReadLimit(realtime.MaxFrameBytes)
	return c, ctx, cancel
}

func TestProxyRelaysBidirectionally(t *testing.T) {
	upstream := echoServer(t)
	defer upstream.Close()

	var mu sync.Mutex
	var serverFrames [][]byte
	tap := func(p []byte) {
		mu.Lock()
		serverFrames = append(serverFrames, append([]byte(nil), p...))
		mu.Unlock()
	}
	retc := make(chan error, 1)
	proxy := proxyServer(t, wsURL(upstream.URL), tap, retc)
	defer proxy.Close()

	client, ctx, cancel := dialClient(t, proxy.URL)
	defer cancel()

	if err := client.Write(ctx, websocket.MessageText, []byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	typ, data, err := client.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if typ != websocket.MessageText || string(data) != "ping" {
		t.Errorf("got (%v,%q), want (text,ping)", typ, data)
	}

	client.Close(websocket.StatusNormalClosure, "")
	if got := waitProxy(t, retc); got != nil {
		t.Errorf("Proxy returned %v, want nil on normal close", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(serverFrames) != 1 || string(serverFrames[0]) != "ping" {
		t.Errorf("server tap = %v, want one echoed ping frame", serverFrames)
	}
}

func TestProxyRelaysLargeFrame(t *testing.T) {
	upstream := echoServer(t)
	defer upstream.Close()
	// Wait for Proxy to return: Accept hijacks the connection, so
	// httptest.Server.Close does not wait for the relay goroutines, and a
	// session leaking past the test races later tests' state.
	retc := make(chan error, 1)
	proxy := proxyServer(t, wsURL(upstream.URL), nil, retc)
	defer proxy.Close()

	client, ctx, cancel := dialClient(t, proxy.URL)
	defer cancel()

	// 512 KiB — well beyond coder/websocket's 32 KiB default read limit, which the
	// proxy raises so base64 audio frames survive.
	big := strings.Repeat("a", 512*1024)
	if err := client.Write(ctx, websocket.MessageText, []byte(big)); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, data, err := client.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(data) != len(big) {
		t.Errorf("echoed length = %d, want %d", len(data), len(big))
	}
	client.Close(websocket.StatusNormalClosure, "")
	if got := waitProxy(t, retc); got != nil {
		t.Errorf("Proxy returned %v, want nil on normal close", got)
	}
}

func TestProxyDialErrorBeforeUpgrade(t *testing.T) {
	// Point the proxy at an address that cannot be dialed; Proxy must return a
	// *DialError before upgrading the client so the caller can write an HTTP error.
	retc := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := realtime.Proxy(w, r, realtime.Target{URL: "ws://127.0.0.1:1/realtime"}, nil)
		retc <- err
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, wsURL(srv.URL), nil)
	if err == nil {
		t.Fatal("expected client dial to fail against a non-upgraded response")
	}
	if resp != nil && resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
	var de *realtime.DialError
	if got := waitProxy(t, retc); !errors.As(got, &de) {
		t.Fatalf("Proxy error = %v, want *DialError", got)
	}
}

func waitProxy(t *testing.T, retc chan error) error {
	t.Helper()
	select {
	case err := <-retc:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Proxy to return")
		return nil
	}
}

func TestProxyHeartbeatTearsDownUnresponsivePeer(t *testing.T) {
	restore := realtime.SetHeartbeatCadenceForTest(30*time.Millisecond, 150*time.Millisecond)
	defer restore()

	upstream := echoServer(t)
	defer upstream.Close()

	retc := make(chan error, 1)
	proxy := proxyServer(t, wsURL(upstream.URL), nil, retc)
	defer proxy.Close()

	// Connect and go silent: coder/websocket only answers pings while a Read
	// is in flight, so a client that never reads models a dead peer (NAT
	// timeout, power loss) that keeps the TCP connection nominally open.
	client, _, cancel := dialClient(t, proxy.URL)
	defer cancel()
	defer client.Close(websocket.StatusNormalClosure, "")

	select {
	case err := <-retc:
		if err == nil || !strings.Contains(err.Error(), "heartbeat") {
			t.Fatalf("Proxy returned %v, want heartbeat failure", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session with unresponsive peer was not torn down")
	}
}

func TestProxyHeartbeatLeavesResponsiveSessionAlive(t *testing.T) {
	// Short interval so several pings land inside the loop below, but a
	// generous pong timeout: a scheduler stall on a loaded CI runner must not
	// fail a healthy session.
	restore := realtime.SetHeartbeatCadenceForTest(25*time.Millisecond, 2*time.Second)
	defer restore()

	upstream := echoServer(t)
	defer upstream.Close()

	retc := make(chan error, 1)
	proxy := proxyServer(t, wsURL(upstream.URL), nil, retc)
	defer proxy.Close()

	client, ctx, cancel := dialClient(t, proxy.URL)
	defer cancel()

	// Exchange frames across several heartbeat intervals: an active session
	// (reads in flight on both sides answer the pings) must not be killed.
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		if err := client.Write(ctx, websocket.MessageText, []byte(`{"ping":"pong"}`)); err != nil {
			t.Fatalf("client write failed: %v", err)
		}
		if _, _, err := client.Read(ctx); err != nil {
			t.Fatalf("client read failed: %v", err)
		}
	}

	select {
	case err := <-retc:
		t.Fatalf("session ended early: %v", err)
	default:
	}
	if err := client.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatalf("client close failed: %v", err)
	}
	select {
	case err := <-retc:
		if err != nil {
			t.Fatalf("Proxy returned %v after normal close, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("proxy did not finish after client close")
	}
}
