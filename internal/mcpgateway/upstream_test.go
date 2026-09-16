package mcpgateway

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// TestUpstreamConnectsToStatelessServer covers servers that run streamable
// HTTP without sessions (mcp-devtools, Firecrawl): the dial must succeed with
// no Mcp-Session-Id, list the catalog, and forward tool calls.
func TestUpstreamConnectsToStatelessServer(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "alpha", Version: "test"}, nil)
	addEchoTool("echo")(server)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	u := newUpstream(testSpec("alpha", ts.URL, nil), ts.Client())
	// Registered after ts.Close so it runs first: the upstream holds a
	// long-lived stream the test server would otherwise wait on.
	t.Cleanup(u.close)

	ctx := context.Background()
	err := u.refresh(ctx)
	require.NoError(t, err)

	cat, status := u.snapshot()
	require.Equal(t, StatusConnected, status)
	require.Equal(t, 1, cat.toolCount())

	res, err := u.callTool(ctx, "echo", []byte(`{"text":"hi"}`))
	require.NoError(t, err)

	text, ok := res.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	require.Equal(t, `echo:{"text":"hi"}`, text.Text, "callTool() content = %#v, want echoed arguments", res.Content[0])
}

// TestUpstreamConnectNamesMissingEndpoint covers a URL whose path serves no
// MCP endpoint (the usual misconfiguration): the error must point at the
// path instead of surfacing the SDK's bare "Not Found", naming only the
// origin so credentials in the path or query never reach logs.
func TestUpstreamConnectNamesMissingEndpoint(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(ts.Close)

	u := newUpstream(testSpec("alpha", ts.URL+"/path-secret?token=query-secret", nil), ts.Client())
	err := u.refresh(context.Background())
	require.Error(t, err)

	for _, want := range []string{`connect to mcp server "alpha"`, ts.URL + " answered HTTP 404 Not Found", "check the url"} {
		require.Contains(t, err.Error(), want)
	}
	for _, leak := range []string{"path-secret", "query-secret"} {
		require.NotContains(t, err.Error(), leak, "error = %q leaks %q from the URL", err, leak)
	}
	view := u.view()
	require.Equal(t, StatusDegraded, view.Status)
	require.Equal(t, err.Error(), view.LastError, "view = %+v, want degraded with the connect error", view)
}

// TestUpstreamConnectNamesTransportMismatch covers a 405 on the handshake:
// a streamable HTTP endpoint configured as sse (or the reverse) exists at the
// path, so the hint must point at the transport rather than only the url.
func TestUpstreamConnectNamesTransportMismatch(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "alpha", Version: "test"}, nil)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	u := newUpstream(testSpec("alpha", ts.URL, func(spec *ServerSpec) { spec.Transport = "sse" }), ts.Client())
	err := u.refresh(context.Background())
	require.Error(t, err)

	for _, want := range []string{ts.URL + " answered HTTP 405 Method Not Allowed to the sse handshake", "check the transport and the url"} {
		require.Contains(t, err.Error(), want)
	}
	require.NotContains(t, err.Error(), "no MCP endpoint at that path")
}

// TestUpstreamConnectKeepsSessionLossDiagnosis covers a stateful server that
// accepts initialize and then drops the session: the 404 arrives on a later
// request, so the error must stay the SDK's session failure rather than
// blame a URL that did serve the handshake.
func TestUpstreamConnectKeepsSessionLossDiagnosis(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "alpha", Version: "test"}, nil)
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Mcp-Session-Id") != "" {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	u := newUpstream(testSpec("alpha", ts.URL, nil), ts.Client())
	err := u.refresh(context.Background())
	require.Error(t, err)
	require.NotContains(t, err.Error(), "no MCP endpoint")
	require.Contains(t, err.Error(), `connect to mcp server "alpha"`)
}

// TestUpstreamSSEConnectNamesMissingEndpoint covers the legacy SSE transport,
// whose handshake is a GET: a 404 there must get the same URL hint.
func TestUpstreamSSEConnectNamesMissingEndpoint(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(ts.Close)

	u := newUpstream(testSpec("alpha", ts.URL+"/sse", func(spec *ServerSpec) { spec.Transport = "sse" }), ts.Client())
	err := u.refresh(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), ts.URL+" answered HTTP 404 Not Found")
}

// TestUpstreamConnectRedactsTransportFailure covers a dial that never gets
// a response: the HTTP client's error carries the full URL, and everything
// but the origin must be stripped before it reaches logs or the dashboard.
func TestUpstreamConnectRedactsTransportFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	addr := listener.Addr().String()
	_ = listener.Close()

	for _, transport := range []string{"http", "sse"} {
		t.Run(transport, func(t *testing.T) {
			url := "http://user:user-secret@" + addr + "/path-secret?token=query-secret"
			u := newUpstream(testSpec("alpha", url, func(spec *ServerSpec) { spec.Transport = transport }), nil)
			err := u.refresh(context.Background())
			require.Error(t, err)
			require.Contains(t, err.Error(), "http://"+addr)

			for _, leak := range []string{"user", "path-secret", "query-secret"} {
				require.NotContains(t, err.Error(), leak, "error = %q leaks %q from the URL", err, leak)
			}
			view := u.view()
			require.NotContains(t, view.LastError, "secret", "LastError = %q leaks the URL", view.LastError)

		})
	}
}
