package mcpgateway

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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
	if err := u.refresh(ctx); err != nil {
		t.Fatalf("refresh() against stateless upstream error = %v", err)
	}
	cat, status := u.snapshot()
	if status != StatusConnected || cat.toolCount() != 1 {
		t.Fatalf("status = %v, tools = %d, want connected with 1 tool", status, cat.toolCount())
	}
	res, err := u.callTool(ctx, "echo", []byte(`{"text":"hi"}`))
	if err != nil {
		t.Fatalf("callTool() error = %v", err)
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok || text.Text != `echo:{"text":"hi"}` {
		t.Fatalf("callTool() content = %#v, want echoed arguments", res.Content[0])
	}
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
	if err == nil {
		t.Fatalf("refresh() against a non-MCP path should error")
	}
	for _, want := range []string{`connect to mcp server "alpha"`, ts.URL + " answered HTTP 404 Not Found", "check the url"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to contain %q", err, want)
		}
	}
	for _, leak := range []string{"path-secret", "query-secret"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("error = %q leaks %q from the URL", err, leak)
		}
	}
	view := u.view()
	if view.Status != StatusDegraded || view.LastError != err.Error() {
		t.Fatalf("view = %+v, want degraded with the connect error", view)
	}
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
	if err == nil {
		t.Fatalf("refresh() against a server dropping its session should error")
	}
	if strings.Contains(err.Error(), "no MCP endpoint") {
		t.Fatalf("error = %q misreports a lost session as a missing endpoint", err)
	}
	if !strings.Contains(err.Error(), `connect to mcp server "alpha"`) {
		t.Fatalf("error = %q, want the connect wrapper", err)
	}
}

// TestUpstreamSSEConnectNamesMissingEndpoint covers the legacy SSE transport,
// whose handshake is a GET: a 404 there must get the same URL hint.
func TestUpstreamSSEConnectNamesMissingEndpoint(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(ts.Close)

	u := newUpstream(testSpec("alpha", ts.URL+"/sse", func(spec *ServerSpec) { spec.Transport = "sse" }), ts.Client())
	err := u.refresh(context.Background())
	if err == nil {
		t.Fatalf("refresh() against a non-MCP path should error")
	}
	if !strings.Contains(err.Error(), ts.URL+" answered HTTP 404 Not Found") {
		t.Fatalf("error = %q, want the missing-endpoint hint", err)
	}
}

// TestUpstreamConnectRedactsTransportFailure covers a dial that never gets
// a response: the HTTP client's error carries the full URL, and everything
// but the origin must be stripped before it reaches logs or the dashboard.
func TestUpstreamConnectRedactsTransportFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	for _, transport := range []string{"http", "sse"} {
		t.Run(transport, func(t *testing.T) {
			url := "http://user:user-secret@" + addr + "/path-secret?token=query-secret"
			u := newUpstream(testSpec("alpha", url, func(spec *ServerSpec) { spec.Transport = transport }), nil)
			err := u.refresh(context.Background())
			if err == nil {
				t.Fatalf("refresh() against a closed port should error")
			}
			if !strings.Contains(err.Error(), "http://"+addr) {
				t.Fatalf("error = %q, want the origin kept", err)
			}
			for _, leak := range []string{"user", "path-secret", "query-secret"} {
				if strings.Contains(err.Error(), leak) {
					t.Fatalf("error = %q leaks %q from the URL", err, leak)
				}
			}
			if view := u.view(); strings.Contains(view.LastError, "secret") {
				t.Fatalf("LastError = %q leaks the URL", view.LastError)
			}
		})
	}
}
