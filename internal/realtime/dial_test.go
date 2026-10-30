package realtime_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/realtime"
)

// providerClient mimics a provider's outbound client: it trusts the upstream's
// certificate (a private CA) and routes through a proxy function, which
// records each consultation and then connects directly.
func providerClient(t *testing.T, upstream *httptest.Server) (*http.Client, *atomic.Int32) {
	t.Helper()
	base, ok := upstream.Client().Transport.(*http.Transport)
	require.True(t, ok)
	transport := base.Clone()
	var proxied atomic.Int32
	transport.Proxy = func(*http.Request) (*url.URL, error) {
		proxied.Add(1)
		return nil, nil
	}
	return &http.Client{Transport: transport}, &proxied
}

func TestObserveDialsThroughTheProviderClient(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"session.created"}`))
		conn.Close(websocket.StatusNormalClosure, "")
	}))
	defer upstream.Close()
	target := realtime.Target{URL: wsURL(upstream.URL)}

	// The shared default client does not trust the private CA.
	err := realtime.Observe(context.Background(), target, nil)
	require.Error(t, err, "expected the private CA to be rejected without the provider client")

	client, proxied := providerClient(t, upstream)
	target.HTTPClient = client
	var frames int
	require.NoError(t, realtime.Observe(context.Background(), target, func([]byte) { frames++ }))
	assert.Equal(t, 1, frames)
	assert.Positive(t, proxied.Load(), "the provider client's proxy was not consulted")
}

func TestProxyDialsThroughTheProviderClient(t *testing.T) {
	echo := echoServer(t)
	defer echo.Close()
	upstream := httptest.NewTLSServer(echo.Config.Handler)
	defer upstream.Close()
	client, proxied := providerClient(t, upstream)

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = realtime.Proxy(w, r, realtime.Target{URL: wsURL(upstream.URL), HTTPClient: client}, realtime.Hooks{})
	}))
	defer gateway.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL(gateway.URL), nil)
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()
	require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte("ping")))
	_, data, err := conn.Read(ctx)
	require.NoError(t, err)
	assert.Equal(t, "ping", string(data))
	assert.Positive(t, proxied.Load(), "the provider client's proxy was not consulted")
}
