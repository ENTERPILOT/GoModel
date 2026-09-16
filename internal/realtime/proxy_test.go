package realtime_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	return hookedProxyServer(t, upstreamWS, realtime.Hooks{OnServerFrame: onServerFrame}, retc)
}

// hookedProxyServer is proxyServer with the full set of relay hooks.
func hookedProxyServer(t *testing.T, upstreamWS string, hooks realtime.Hooks, retc chan<- error) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := realtime.Proxy(w, r, realtime.Target{URL: upstreamWS}, hooks)
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
	err := client.Write(ctx, websocket.MessageText, []byte("ping"))
	require.NoError(t, err)

	typ, data, err := client.Read(ctx)
	require.NoError(t, err)
	assert.Equal(t, websocket.MessageText, typ)
	assert.Equal(t, "ping", string(data), "got (%v,%q), want (text,ping)", typ, data)

	client.Close(websocket.StatusNormalClosure, "")
	got := waitProxy(t, retc)
	require.NoError(t, got)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, serverFrames, 1)
	assert.Equal(t, "ping", string(serverFrames[0]))
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
	err := client.Write(ctx, websocket.MessageText, []byte(big))
	require.NoError(t, err)

	_, data, err := client.Read(ctx)
	require.NoError(t, err)
	assert.Len(t, data, len(big))

	client.Close(websocket.StatusNormalClosure, "")
	got := waitProxy(t, retc)
	assert.NoError(t, got)
}

func TestProxyDialErrorBeforeUpgrade(t *testing.T) {
	// Point the proxy at an address that cannot be dialed; Proxy must return a
	// *DialError before upgrading the client so the caller can write an HTTP error.
	retc := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := realtime.Proxy(w, r, realtime.Target{URL: "ws://127.0.0.1:1/realtime"}, realtime.Hooks{})
		retc <- err
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, wsURL(srv.URL), nil)
	require.Error(t, err)

	if resp != nil {
		assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
	}
	var de *realtime.DialError
	got := waitProxy(t, retc)
	require.ErrorAs(t, got, &de)
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
		require.Error(t, err)
		require.Contains(t, err.Error(), "heartbeat")

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
		err := client.Write(ctx, websocket.MessageText, []byte(`{"ping":"pong"}`))
		require.NoError(t, err)
		_, _, err = client.Read(ctx)
		require.NoError(t, err)
	}

	select {
	case err := <-retc:
		t.Fatalf("session ended early: %v", err)
	default:
	}
	err := client.Close(websocket.StatusNormalClosure, "")
	require.NoError(t, err)

	select {
	case err := <-retc:
		require.NoError(t, err)

	case <-time.After(5 * time.Second):
		t.Fatal("proxy did not finish after client close")
	}
}

func TestProxyMapsClientFrames(t *testing.T) {
	// The client->upstream mapper carries session policy (e.g. pinning the
	// transcription model), so the upstream must see the mapped frame while the
	// echoed reply proves the session still relays normally.
	upstream := echoServer(t)
	defer upstream.Close()
	proxy := hookedProxyServer(t, wsURL(upstream.URL), realtime.Hooks{
		MapClientFrame: func(frame []byte) []byte {
			return bytes.ReplaceAll(frame, []byte("client"), []byte("mapped"))
		},
	}, nil)
	defer proxy.Close()

	c, ctx, cancel := dialClient(t, proxy.URL)
	defer cancel()
	defer c.Close(websocket.StatusNormalClosure, "done")
	err := c.Write(ctx, websocket.MessageText, []byte(`{"from":"client"}`))
	require.NoError(t, err)

	_, echoed, err := c.Read(ctx)
	require.NoError(t, err)
	got := string(echoed)
	assert.Equal(t, `{"from":"mapped"}`, got)
}

func TestProxyObservesClientFrames(t *testing.T) {
	// The client->upstream observer backs input metering, so it must see each
	// frame as the client sent it, before any mapping rewrites it.
	upstream := echoServer(t)
	defer upstream.Close()
	observed := make(chan []byte, 1)
	proxy := hookedProxyServer(t, wsURL(upstream.URL), realtime.Hooks{
		OnClientFrame: func(frame []byte) { observed <- bytes.Clone(frame) },
		MapClientFrame: func(frame []byte) []byte {
			return bytes.ReplaceAll(frame, []byte("client"), []byte("mapped"))
		},
	}, nil)
	defer proxy.Close()

	c, ctx, cancel := dialClient(t, proxy.URL)
	defer cancel()
	defer c.Close(websocket.StatusNormalClosure, "done")
	err := c.Write(ctx, websocket.MessageText, []byte(`{"from":"client"}`))
	require.NoError(t, err)

	select {
	case frame := <-observed:
		got := string(frame)
		assert.Equal(t, `{"from":"client"}`, got)

	case <-time.After(5 * time.Second):
		t.Fatal("client frame was never observed")
	}
	_, _, err = c.Read(ctx)
	require.NoError(t, err)
}
