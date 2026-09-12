package mcpgateway

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/usage"
)

// newTestUpstream serves a real MCP server over streamable HTTP and returns
// its base URL.
func newTestUpstream(t *testing.T, name string, configure func(*mcp.Server)) string {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: name, Version: "test"}, &mcp.ServerOptions{
		Instructions: "instructions from " + name,
	})
	if configure != nil {
		configure(server)
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts.URL
}

func addEchoTool(name string) func(*mcp.Server) {
	return func(server *mcp.Server) {
		server.AddTool(&mcp.Tool{
			Name:        name,
			Description: "echoes its input",
			InputSchema: map[string]any{"type": "object"},
		}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "echo:" + string(req.Params.Arguments)}},
			}, nil
		})
	}
}

// recordingUsageLogger captures usage entries written by the gateway.
type recordingUsageLogger struct {
	mu      sync.Mutex
	entries []*usage.UsageEntry
}

func (l *recordingUsageLogger) Write(entry *usage.UsageEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, entry)
}

func (l *recordingUsageLogger) Config() usage.Config { return usage.Config{Enabled: true} }
func (l *recordingUsageLogger) Close() error         { return nil }

func (l *recordingUsageLogger) all() []*usage.UsageEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]*usage.UsageEntry(nil), l.entries...)
}

func testSpec(name, url string, mutate func(*ServerSpec)) ServerSpec {
	spec := ServerSpec{
		Name:        name,
		URL:         url,
		Transport:   config.MCPTransportHTTP,
		Enabled:     true,
		ToolTimeout: 10 * time.Second,
		Managed:     true,
	}
	if mutate != nil {
		mutate(&spec)
	}
	return spec
}

// newTestService builds a Service over the given specs and returns it with a
// gateway HTTP endpoint that mimics GoModel's ingress: bearer auth is assumed
// done; the user path is read from the standard header into the context, and
// /mcp/{server} pins a single upstream.
func newTestService(t *testing.T, usageLogger usage.LoggerInterface, specs ...ServerSpec) (*Service, string) {
	t.Helper()
	configServers := make(map[string]ServerSpec, len(specs))
	for _, spec := range specs {
		configServers[spec.Name] = spec
	}
	service, err := NewService(context.Background(), Options{
		ConfigServers: configServers,
		UsageLogger:   usageLogger,
	})
	require.NoError(t, err)

	t.Cleanup(service.Close)

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authKeyID := r.Header.Get("X-Test-Auth-Key-ID"); authKeyID != "" {
			r = r.WithContext(core.WithAuthKeyID(r.Context(), authKeyID))
		}
		if userPath := r.Header.Get(core.UserPathHeader); userPath != "" {
			r = r.WithContext(core.WithEffectiveUserPath(r.Context(), userPath))
		}
		pinned := ""
		if rest, ok := strings.CutPrefix(r.URL.Path, "/mcp/"); ok {
			pinned = rest
		}
		if err := service.ServeHTTP(w, r, pinned); err != nil {
			status := http.StatusInternalServerError
			if gatewayErr, ok := errors.AsType[*core.GatewayError](err); ok {
				status = gatewayErr.HTTPStatusCode()
			}
			http.Error(w, err.Error(), status)
		}
	}))
	t.Cleanup(gateway.Close)

	waitForConnected(t, service, len(specs))
	return service, gateway.URL
}

// waitForConnected waits until every enabled server reports a terminal
// status (connected or degraded), so tests do not race the async connect.
func waitForConnected(t *testing.T, service *Service, servers int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		views := service.Views()
		settled := 0
		for _, view := range views {
			if !view.Spec.Enabled || view.Status == StatusConnected || view.Status == StatusDegraded {
				settled++
			}
		}
		if len(views) == servers && settled == servers {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("upstreams did not settle: %+v", service.Views())
}

func connectClient(t *testing.T, endpoint string, headers map[string]string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint: endpoint,
		HTTPClient: &http.Client{Transport: &headerRoundTripper{
			base: http.DefaultTransport, headers: headers, origin: requestOrigin(endpoint),
		}},
	}
	session, err := client.Connect(context.Background(), transport, nil)
	require.NoError(t, err, "client connect to %s: %v", endpoint, err)

	t.Cleanup(func() { _ = session.Close() })
	return session
}

const testInitializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`

func rawMCPPost(t *testing.T, endpoint, body string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	return resp
}

func initializeRawSession(t *testing.T, endpoint string, headers map[string]string) string {
	t.Helper()
	resp := rawMCPPost(t, endpoint, testInitializeBody, headers)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	sessionID := resp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID)

	return sessionID
}

func rawMCPStatus(t *testing.T, endpoint, body string, headers map[string]string) int {
	t.Helper()
	resp := rawMCPPost(t, endpoint, body, headers)
	defer resp.Body.Close()
	return resp.StatusCode
}

func listToolNames(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()
	result, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)

	names := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func TestAggregatedEndpointNamespacesAndRelays(t *testing.T) {
	alphaURL := newTestUpstream(t, "alpha", addEchoTool("echo"))
	betaURL := newTestUpstream(t, "beta", addEchoTool("search"))
	usageLog := &recordingUsageLogger{}
	_, gatewayURL := newTestService(t, usageLog,
		testSpec("alpha", alphaURL, nil),
		testSpec("beta", betaURL, nil),
	)

	session := connectClient(t, gatewayURL+"/mcp", map[string]string{
		core.UserPathHeader: "/team-a",
		"X-Request-ID":      "req-123",
	})

	names := listToolNames(t, session)
	want := []string{"alpha_echo", "beta_search"}
	require.Len(t, names, 2)
	require.Equal(t, want[0], names[0])
	require.Equal(t, want[1], names[1], "ListTools() = %v, want %v", names, want)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "alpha_echo",
		Arguments: map[string]any{"value": 1},
	})
	require.NoError(t, err)
	require.Len(t, result.Content, 1)

	text, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	require.True(t, strings.HasPrefix(text.Text, "echo:"), "CallTool content = %#v, want echo:... text", result.Content[0])

	// The tool call must be attributed in the usage pipeline.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(usageLog.all()) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	entries := usageLog.all()
	require.Len(t, entries, 1)

	entry := entries[0]
	require.Equal(t, "mcp", entry.Provider)
	require.Equal(t, "alpha", entry.ProviderName)
	require.Equal(t, "alpha_echo", entry.Model)
	require.Equal(t, "/team-a", entry.UserPath)
	require.Equal(t, "req-123", entry.RequestID)
}

func TestAggregatedEndpointAcceptsUniqueBareToolName(t *testing.T) {
	alphaURL := newTestUpstream(t, "alpha", addEchoTool("echo"))
	betaURL := newTestUpstream(t, "beta", addEchoTool("search"))
	usageLog := &recordingUsageLogger{}
	_, gatewayURL := newTestService(t, usageLog,
		testSpec("alpha", alphaURL, nil),
		testSpec("beta", betaURL, nil),
	)

	session := connectClient(t, gatewayURL+"/mcp", nil)

	// tools/list stays namespaced; only tools/call accepts the bare name.
	names := listToolNames(t, session)
	require.Len(t, names, 2)
	require.Equal(t, "alpha_echo", names[0])
	require.Equal(t, "beta_search", names[1])

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "echo",
		Arguments: map[string]any{"value": 1},
	})
	require.NoError(t, err)

	text, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	require.True(t, strings.HasPrefix(text.Text, "echo:"), "CallTool(echo) content = %#v, want echo:... text", result.Content[0])

	// Usage accounting stays canonical: the entry records the namespaced name.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(usageLog.all()) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	entries := usageLog.all()
	require.Len(t, entries, 1)
	require.Equal(t, "alpha_echo", entries[0].Model)
	require.Equal(t, "alpha", entries[0].ProviderName)
}

func TestAggregatedEndpointRejectsAmbiguousBareToolName(t *testing.T) {
	alphaURL := newTestUpstream(t, "alpha", addEchoTool("echo"))
	betaURL := newTestUpstream(t, "beta", addEchoTool("echo"))
	_, gatewayURL := newTestService(t, nil,
		testSpec("alpha", alphaURL, nil),
		testSpec("beta", betaURL, nil),
	)

	session := connectClient(t, gatewayURL+"/mcp", nil)
	_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "echo"})
	require.Error(t, err)

	// The namespaced forms keep working.
	for _, name := range []string{"alpha_echo", "beta_echo"} {
		_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name})
		require.NoError(t, err)
	}
}

func TestPerServerEndpointKeepsOriginalNames(t *testing.T) {
	alphaURL := newTestUpstream(t, "alpha", addEchoTool("echo"))
	betaURL := newTestUpstream(t, "beta", addEchoTool("search"))
	_, gatewayURL := newTestService(t, nil,
		testSpec("alpha", alphaURL, nil),
		testSpec("beta", betaURL, nil),
	)

	session := connectClient(t, gatewayURL+"/mcp/alpha", nil)
	names := listToolNames(t, session)
	require.Len(t, names, 1)
	require.Equal(t, "echo", names[0])

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "echo"})
	require.NoError(t, err)
	require.False(t, result.IsError)
}

func TestUnknownPinnedServerReturns404(t *testing.T) {
	alphaURL := newTestUpstream(t, "alpha", addEchoTool("echo"))
	_, gatewayURL := newTestService(t, nil, testSpec("alpha", alphaURL, nil))

	resp, err := http.Post(gatewayURL+"/mcp/ghost", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)

	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestUserPathVisibilityFiltersServers(t *testing.T) {
	alphaURL := newTestUpstream(t, "alpha", addEchoTool("echo"))
	betaURL := newTestUpstream(t, "beta", addEchoTool("search"))
	_, gatewayURL := newTestService(t, nil,
		testSpec("alpha", alphaURL, nil),
		testSpec("beta", betaURL, func(spec *ServerSpec) {
			spec.UserPaths = []string{"/team-b"}
		}),
	)

	// A /team-a caller must not discover beta's tools.
	teamA := connectClient(t, gatewayURL+"/mcp", map[string]string{core.UserPathHeader: "/team-a"})
	names := listToolNames(t, teamA)
	require.Len(t, names, 1)
	require.Equal(t, "alpha_echo", names[0])
	_, err := teamA.CallTool(context.Background(), &mcp.CallToolParams{Name: "beta_search"})
	require.Error(t, err)

	// A /team-b/dev caller inherits the /team-b subtree scope.
	teamB := connectClient(t, gatewayURL+"/mcp", map[string]string{core.UserPathHeader: "/team-b/dev"})
	names = listToolNames(t, teamB)
	require.Len(t, names, 2)

	// A pinned endpoint for an out-of-scope server is a 404.
	resp, err := http.Post(gatewayURL+"/mcp/beta", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)

	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestScopeHeaderNarrowsVisibleServers(t *testing.T) {
	alphaURL := newTestUpstream(t, "alpha", addEchoTool("echo"))
	betaURL := newTestUpstream(t, "beta", addEchoTool("search"))
	_, gatewayURL := newTestService(t, nil,
		testSpec("alpha", alphaURL, nil),
		testSpec("beta", betaURL, nil),
	)

	session := connectClient(t, gatewayURL+"/mcp", map[string]string{ScopeHeader: "beta, ghost"})
	names := listToolNames(t, session)
	require.Len(t, names, 1)
	require.Equal(t, "beta_search", names[0])
}

func TestToolFiltersHideTools(t *testing.T) {
	url := newTestUpstream(t, "alpha", func(server *mcp.Server) {
		addEchoTool("read")(server)
		addEchoTool("write")(server)
		addEchoTool("admin")(server)
	})
	_, gatewayURL := newTestService(t, nil,
		testSpec("alpha", url, func(spec *ServerSpec) {
			spec.AllowedTools = []string{"read", "write"}
			spec.DisallowedTools = []string{"write"}
		}),
	)

	session := connectClient(t, gatewayURL+"/mcp", nil)
	names := listToolNames(t, session)
	require.Len(t, names, 1)
	require.Equal(t, "alpha_read", names[0])
}

func TestSessionBindingRejectsForeignUserPath(t *testing.T) {
	alphaURL := newTestUpstream(t, "alpha", addEchoTool("echo"))
	_, gatewayURL := newTestService(t, nil, testSpec("alpha", alphaURL, nil))

	sessionID := initializeRawSession(t, gatewayURL+"/mcp", map[string]string{
		core.UserPathHeader: "/team-a",
	})
	listBody := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	status := rawMCPStatus(t, gatewayURL+"/mcp", listBody, map[string]string{
		"Mcp-Session-Id":    sessionID,
		core.UserPathHeader: "/team-b",
	})
	require.Equal(t, http.StatusNotFound, status)
}

func TestSessionBindingRejectsForeignAuthKey(t *testing.T) {
	alphaURL := newTestUpstream(t, "alpha", addEchoTool("echo"))
	_, gatewayURL := newTestService(t, nil, testSpec("alpha", alphaURL, nil))

	sessionID := initializeRawSession(t, gatewayURL+"/mcp", map[string]string{"X-Test-Auth-Key-ID": "key-a"})
	listBody := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	status := rawMCPStatus(t, gatewayURL+"/mcp", listBody, map[string]string{
		"Mcp-Session-Id":     sessionID,
		"X-Test-Auth-Key-ID": "key-b",
	})
	require.Equal(t, http.StatusNotFound, status)
}

func TestSessionBindingRejectsDifferentPinnedEndpoint(t *testing.T) {
	alphaURL := newTestUpstream(t, "alpha", addEchoTool("echo"))
	betaURL := newTestUpstream(t, "beta", addEchoTool("search"))
	_, gatewayURL := newTestService(t, nil,
		testSpec("alpha", alphaURL, nil),
		testSpec("beta", betaURL, nil),
	)

	sessionID := initializeRawSession(t, gatewayURL+"/mcp/alpha", nil)
	listBody := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	status := rawMCPStatus(t, gatewayURL+"/mcp/beta", listBody, map[string]string{"Mcp-Session-Id": sessionID})
	require.Equal(t, http.StatusNotFound, status)
}

func TestStreamableHTTPRejectsCrossOriginRequests(t *testing.T) {
	alphaURL := newTestUpstream(t, "alpha", addEchoTool("echo"))
	_, gatewayURL := newTestService(t, nil, testSpec("alpha", alphaURL, nil))

	status := rawMCPStatus(t, gatewayURL+"/mcp", testInitializeBody, map[string]string{
		"Origin": "https://attacker.example",
	})
	require.Equal(t, http.StatusForbidden, status)
}

func TestDownstreamCapabilitiesDoNotPromiseListChanged(t *testing.T) {
	alphaURL := newTestUpstream(t, "alpha", addEchoTool("echo"))
	_, gatewayURL := newTestService(t, nil, testSpec("alpha", alphaURL, nil))

	session := connectClient(t, gatewayURL+"/mcp", nil)
	init := session.InitializeResult()
	require.NotNil(t, init)
	require.NotNil(t, init.Capabilities)
	require.NotNil(t, init.Capabilities.Tools)
	require.False(t, init.Capabilities.Tools.ListChanged)
	require.NotNil(t, init.Capabilities.Prompts)
	require.False(t, init.Capabilities.Prompts.ListChanged)
	require.NotNil(t, init.Capabilities.Resources)
	require.False(t, init.Capabilities.Resources.ListChanged)
}

func TestUpstreamHeadersStayOnConfiguredOrigin(t *testing.T) {
	var redirectedHeader string
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedHeader = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer redirectTarget.Close()

	var sameOriginHeader string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, redirectTarget.URL, http.StatusTemporaryRedirect)
			return
		}
		sameOriginHeader = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer origin.Close()

	u := newUpstream(ServerSpec{
		Name: "headers", URL: origin.URL, Transport: "http", Enabled: true,
		Headers: map[string]string{"Authorization": "Bearer upstream-secret"},
	}, http.DefaultClient)
	client := u.httpClientWithHeaders()
	resp, err := client.Get(origin.URL + "/same-origin")
	require.NoError(t, err)

	_ = resp.Body.Close()
	resp, err = client.Get(origin.URL + "/redirect")
	require.NoError(t, err)

	_ = resp.Body.Close()
	require.Equal(t, "Bearer upstream-secret", sameOriginHeader)
	require.Empty(t, redirectedHeader)
}

func TestDegradedUpstreamKeepsCatalogAndReportsError(t *testing.T) {
	upstream := mcp.NewServer(&mcp.Implementation{Name: "alpha", Version: "test"}, nil)
	addEchoTool("echo")(upstream)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return upstream }, nil)
	ts := httptest.NewServer(handler)

	service, gatewayURL := newTestService(t, nil, testSpec("alpha", ts.URL, nil))

	// Kill the upstream, then force a reconnect: the server must degrade but
	// keep its last known catalog (stale carry-forward, not empty-connected).
	// The gateway still holds the upstream's standalone SSE stream, so drop
	// live connections before Close (which waits for outstanding requests).
	ts.CloseClientConnections()
	ts.Close()
	_, err := service.Reconnect(context.Background(), "alpha")
	require.Error(t, err)

	views := service.Views()
	require.Len(t, views, 1)
	require.Equal(t, StatusDegraded, views[0].Status)
	require.Equal(t, 1, views[0].ToolCount)
	require.NotEmpty(t, views[0].LastError)

	// New sessions still see the stale catalog; calling the tool fails with a
	// JSON-RPC error, never a fabricated result.
	session := connectClient(t, gatewayURL+"/mcp", nil)
	names := listToolNames(t, session)
	require.Len(t, names, 1)
	require.Equal(t, "alpha_echo", names[0])
	_, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "alpha_echo"})
	require.Error(t, err)
}

// TestDegradedUpstreamRecoversOnReprobe covers the background re-probe path:
// after the upstream dies and comes back on the same address, a plain refresh
// (what the maintenance loop runs) must recover without a manual reconnect.
func TestDegradedUpstreamRecoversOnReprobe(t *testing.T) {
	upstream := mcp.NewServer(&mcp.Implementation{Name: "alpha", Version: "test"}, nil)
	addEchoTool("echo")(upstream)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return upstream }, nil)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	addr := listener.Addr().String()
	ts1 := &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler}}
	ts1.Start()

	service, _ := newTestService(t, nil, testSpec("alpha", "http://"+addr, nil))
	u, ok := service.manager.get("alpha")
	require.True(t, ok)

	// Kill the upstream and drive one failed refresh (the re-probe path).
	ts1.CloseClientConnections()
	ts1.Close()
	require.Error(t, u.refresh(context.Background()))
	_, status := u.snapshot()
	require.Equal(t, StatusDegraded, status)

	// Resurrect the upstream on the same address; the port was just freed by
	// this process, so rebinding may need a brief retry.
	var listener2 net.Listener
	deadline := time.Now().Add(5 * time.Second)
	for {
		listener2, err = net.Listen("tcp", addr)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.NoError(t, err, "re-listen on %s: %v", addr, err)

	ts2 := &httptest.Server{Listener: listener2, Config: &http.Server{Handler: handler}}
	ts2.Start()
	t.Cleanup(func() {
		ts2.CloseClientConnections()
		ts2.Close()
	})
	// The next refresh (as the maintenance ticker would run it) must redial
	// from scratch and flip the server back to connected.
	err = u.refresh(context.Background())
	require.NoError(t, err)

	view := u.view()
	require.Equal(t, StatusConnected, view.Status)
	require.Equal(t, 1, view.ToolCount, "view after recovery = %+v, want connected with 1 tool", view)
}

// TestRemovedUpstreamRefusesRedial covers the reconcile race: a background
// refresh racing an Apply that removed the server must not dial and leak an
// untracked session.
func TestRemovedUpstreamRefusesRedial(t *testing.T) {
	alphaURL := newTestUpstream(t, "alpha", addEchoTool("echo"))
	service, _ := newTestService(t, nil, testSpec("alpha", alphaURL, nil))

	u, ok := service.manager.get("alpha")
	require.True(t, ok)

	// Reconcile away the server, then run the stale refresh a background
	// goroutine could still be holding.
	service.manager.Apply(nil)
	err := u.refresh(context.Background())
	require.NoError(t, err)

	u.stateMu.Lock()
	session := u.session
	u.stateMu.Unlock()
	require.Nil(t, session)
	_, err = u.callTool(context.Background(), "echo", nil)
	require.Error(t, err)
}

// TestCloseDuringDialDiscardsSession covers the dial-vs-close race: close()
// deliberately does not wait for an in-flight dial, so a dial completing
// after disposal must discard its fresh session instead of storing (and
// leaking) it.
func TestCloseDuringDialDiscardsSession(t *testing.T) {
	upstream := mcp.NewServer(&mcp.Implementation{Name: "alpha", Version: "test"}, nil)
	addEchoTool("echo")(upstream)
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return upstream }, nil)

	// Gate the first request so the dial is reliably in flight when close runs.
	dialEntered := make(chan struct{})
	releaseDial := make(chan struct{})
	var gateOnce sync.Once
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gateOnce.Do(func() {
			close(dialEntered)
			<-releaseDial
		})
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		ts.CloseClientConnections()
		ts.Close()
	})

	u := newUpstream(testSpec("alpha", ts.URL, nil), nil)

	refreshDone := make(chan error, 1)
	go func() { refreshDone <- u.refresh(context.Background()) }()

	<-dialEntered
	u.close()
	close(releaseDial)

	require.Error(t, <-refreshDone)

	u.stateMu.Lock()
	session := u.session
	u.stateMu.Unlock()
	require.Nil(t, session)
}

func TestCloseCancelsActiveDownstreamRequests(t *testing.T) {
	service, err := NewService(context.Background(), Options{})
	require.NoError(t, err)

	requestStarted := make(chan struct{})
	requestDone := make(chan error, 1)
	service.handler = http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-r.Context().Done()
	})
	go func() {
		req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
		requestDone <- service.ServeHTTP(httptest.NewRecorder(), req, "")
	}()

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("downstream request did not start")
	}

	service.Close()
	service.Close() // Close remains safe when the subsystem owner closes again.

	select {
	case err := <-requestDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel the downstream request")
	}
}

func TestInstructionsComposeFromUpstreams(t *testing.T) {
	alphaURL := newTestUpstream(t, "alpha", addEchoTool("echo"))
	_, gatewayURL := newTestService(t, nil, testSpec("alpha", alphaURL, nil))

	session := connectClient(t, gatewayURL+"/mcp", nil)
	init := session.InitializeResult()
	require.NotNil(t, init)
	require.Contains(t, init.Instructions, "instructions from alpha")
	require.Contains(t, init.Instructions, "GoModel MCP gateway")
}

func TestPromptsAndResourcesRelay(t *testing.T) {
	url := newTestUpstream(t, "alpha", func(server *mcp.Server) {
		addEchoTool("echo")(server)
		server.AddPrompt(&mcp.Prompt{Name: "greet", Description: "greeting"}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{
				Messages: []*mcp.PromptMessage{{
					Role:    "user",
					Content: &mcp.TextContent{Text: "hello from " + req.Params.Name},
				}},
			}, nil
		})
		server.AddResource(&mcp.Resource{
			URI:      "file:///alpha/readme",
			Name:     "readme",
			MIMEType: "text/plain",
		}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{
				Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: "text/plain", Text: "readme body"}},
			}, nil
		})
	})
	_, gatewayURL := newTestService(t, nil, testSpec("alpha", url, nil))

	session := connectClient(t, gatewayURL+"/mcp", nil)

	prompts, err := session.ListPrompts(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, prompts.Prompts, 1)
	require.Equal(t, "alpha_greet", prompts.Prompts[0].Name)

	prompt, err := session.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "alpha_greet"})
	require.NoError(t, err)

	text, ok := prompt.Messages[0].Content.(*mcp.TextContent)
	require.True(t, ok)
	require.Equal(t, "hello from greet", text.Text, "GetPrompt content = %#v, want original prompt name forwarded", prompt.Messages[0].Content)

	resources, err := session.ListResources(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, resources.Resources, 1)
	require.Equal(t, "file:///alpha/readme", resources.Resources[0].URI)

	read, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "file:///alpha/readme"})
	require.NoError(t, err)
	require.Len(t, read.Contents, 1)
	require.Equal(t, "readme body", read.Contents[0].Text)
}

func TestUpstreamToolErrorRelaysVerbatim(t *testing.T) {
	url := newTestUpstream(t, "alpha", func(server *mcp.Server) {
		server.AddTool(&mcp.Tool{
			Name:        "fail",
			InputSchema: map[string]any{"type": "object"},
		}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: "tool blew up"}},
			}, nil
		})
	})
	_, gatewayURL := newTestService(t, nil, testSpec("alpha", url, nil))

	session := connectClient(t, gatewayURL+"/mcp", nil)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "alpha_fail"})
	require.NoError(t, err)
	require.True(t, result.IsError)
}

func TestDisabledServerIsInvisible(t *testing.T) {
	alphaURL := newTestUpstream(t, "alpha", addEchoTool("echo"))
	betaURL := newTestUpstream(t, "beta", addEchoTool("search"))
	_, gatewayURL := newTestService(t, nil,
		testSpec("alpha", alphaURL, nil),
		testSpec("beta", betaURL, func(spec *ServerSpec) { spec.Enabled = false }),
	)

	session := connectClient(t, gatewayURL+"/mcp", nil)
	names := listToolNames(t, session)
	require.Len(t, names, 1)
	require.Equal(t, "alpha_echo", names[0])
}
