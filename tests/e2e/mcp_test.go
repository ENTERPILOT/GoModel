//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/mcpgateway"
)

// startMockMCPServer serves a real MCP upstream (built with the same SDK the
// gateway uses) over streamable HTTP.
func startMockMCPServer(t *testing.T, name string, tools ...string) *httptest.Server {
	t.Helper()
	upstream := sdk.NewServer(&sdk.Implementation{Name: name, Version: "e2e"}, nil)
	for _, tool := range tools {
		toolName := tool
		upstream.AddTool(&sdk.Tool{
			Name:        toolName,
			Description: "e2e " + toolName,
			InputSchema: map[string]any{"type": "object"},
		}, func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{
				Content: []sdk.Content{&sdk.TextContent{Text: toolName + ":" + string(req.Params.Arguments)}},
			}, nil
		})
	}
	ts := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return upstream }, nil))
	t.Cleanup(ts.Close)
	return ts
}

func newE2EMCPGateway(t *testing.T, specs map[string]mcpgateway.ServerSpec) *mcpgateway.Service {
	t.Helper()
	service, err := mcpgateway.NewService(context.Background(), mcpgateway.Options{ConfigServers: specs})
	require.NoError(t, err)
	t.Cleanup(service.Close)

	require.Eventually(t, func() bool {
		for _, view := range service.Views() {
			if view.Spec.Enabled && view.Status != mcpgateway.StatusConnected {
				return false
			}
		}
		return true
	}, 10*time.Second, 20*time.Millisecond, "upstream MCP servers did not connect")
	return service
}

func e2eMCPSpec(name, url string) mcpgateway.ServerSpec {
	return mcpgateway.ServerSpec{
		Name:        name,
		URL:         url,
		Transport:   "http",
		Enabled:     true,
		ToolTimeout: 10 * time.Second,
		Managed:     true,
	}
}

// bearerTransport injects the gateway API key the way MCP clients configure
// custom headers.
type bearerTransport struct {
	token string
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if t.token != "" {
		clone.Header.Set("Authorization", "Bearer "+t.token)
	}
	return http.DefaultTransport.RoundTrip(clone)
}

func connectMCPClient(t *testing.T, endpoint, token string) *sdk.ClientSession {
	t.Helper()
	client := sdk.NewClient(&sdk.Implementation{Name: "e2e-client", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{
		Endpoint:   endpoint,
		HTTPClient: &http.Client{Transport: &bearerTransport{token: token}},
	}, nil)
	require.NoError(t, err, "MCP client failed to connect through the gateway")
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// TestMCPGatewayEndToEnd drives a real MCP client through the fully wired
// gateway server: bearer auth, aggregation, namespacing, tool relay.
func TestMCPGatewayEndToEnd(t *testing.T) {
	alpha := startMockMCPServer(t, "alpha", "echo")
	beta := startMockMCPServer(t, "beta", "search", "fetch")
	gateway := newE2EMCPGateway(t, map[string]mcpgateway.ServerSpec{
		"alpha": e2eMCPSpec("alpha", alpha.URL),
		"beta":  e2eMCPSpec("beta", beta.URL),
	})

	srv := setupE2EServer(t, e2eServerOptions{masterKey: "sk-e2e-master", mcpGateway: gateway})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	session := connectMCPClient(t, ts.URL+"/mcp", "sk-e2e-master")

	tools, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	assert.Equal(t, []string{"alpha_echo", "beta_fetch", "beta_search"}, names)

	result, err := session.CallTool(context.Background(), &sdk.CallToolParams{
		Name:      "beta_search",
		Arguments: map[string]any{"q": "gomodel"},
	})
	require.NoError(t, err)
	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(*sdk.TextContent)
	require.True(t, ok)
	assert.True(t, strings.HasPrefix(text.Text, "search:"), "tool result should relay upstream output, got %q", text.Text)
}

// TestMCPGatewayRequiresAuth verifies the MCP routes sit behind the same
// bearer auth as every other model endpoint.
func TestMCPGatewayRequiresAuth(t *testing.T) {
	alpha := startMockMCPServer(t, "alpha", "echo")
	gateway := newE2EMCPGateway(t, map[string]mcpgateway.ServerSpec{
		"alpha": e2eMCPSpec("alpha", alpha.URL),
	})

	srv := setupE2EServer(t, e2eServerOptions{masterKey: "sk-e2e-master", mcpGateway: gateway})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`

	// No key → 401.
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// Wrong key → 401.
	req2, err := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(body))
	require.NoError(t, err)
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Accept", "application/json, text/event-stream")
	req2.Header.Set("Authorization", "Bearer sk-wrong")
	resp2, err := http.DefaultClient.Do(req2)
	require.NoError(t, err)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp2.StatusCode)
}

// TestMCPGatewayPerServerEndpoint verifies /mcp/{server} exposes original
// tool names and unknown servers return 404.
func TestMCPGatewayPerServerEndpoint(t *testing.T) {
	alpha := startMockMCPServer(t, "alpha", "echo")
	gateway := newE2EMCPGateway(t, map[string]mcpgateway.ServerSpec{
		"alpha": e2eMCPSpec("alpha", alpha.URL),
	})

	srv := setupE2EServer(t, e2eServerOptions{masterKey: "sk-e2e-master", mcpGateway: gateway})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	session := connectMCPClient(t, ts.URL+"/mcp/alpha", "sk-e2e-master")
	tools, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, tools.Tools, 1)
	assert.Equal(t, "echo", tools.Tools[0].Name)

	// Unknown pinned server → 404 with an OpenAI-shaped error.
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/mcp/ghost", strings.NewReader(`{}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-e2e-master")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// TestMCPGatewayDisabled verifies that without a wired gateway the routes do
// not exist.
func TestMCPGatewayDisabled(t *testing.T) {
	srv := setupE2EServer(t, e2eServerOptions{masterKey: "sk-e2e-master"})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(`{}`))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer sk-e2e-master")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}
