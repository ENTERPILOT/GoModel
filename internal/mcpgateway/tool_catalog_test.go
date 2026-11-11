package mcpgateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
)

// newToolCatalogTestService serves alpha (one tool hidden by its filters),
// beta (scoped to /team/b), dead (unreachable, hidden from /team/b), and
// gamma (disabled), plus the virtual server "coding".
func newToolCatalogTestService(t *testing.T) *Service {
	t.Helper()
	destructive := true
	noop := func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	}
	alphaURL := newTestUpstream(t, "alpha", func(server *mcp.Server) {
		addEchoTool("echo")(server)
		addEchoTool("wipe")(server)
		server.AddTool(&mcp.Tool{
			Name:        "read",
			InputSchema: map[string]any{"type": "object"},
			Annotations: &mcp.ToolAnnotations{Title: "Read a file", ReadOnlyHint: true},
		}, noop)
		server.AddTool(&mcp.Tool{
			Name:        "drop",
			Description: "drops a table",
			InputSchema: map[string]any{"type": "object"},
			Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive},
		}, noop)
	})
	closed := httptest.NewServer(nil)
	closed.Close()

	service, _ := newTestService(t, nil,
		testSpec("alpha", alphaURL, func(spec *ServerSpec) {
			spec.DisplayName = "Alpha Tools"
			spec.DisallowedTools = []string{"wipe"}
		}),
		testSpec("beta", newTestUpstream(t, "beta", addEchoTool("search")), func(spec *ServerSpec) {
			spec.UserPaths = []string{"/team/b"}
		}),
		testSpec("dead", closed.URL, func(spec *ServerSpec) {
			spec.DisallowedUserPaths = []string{"/team/b"}
		}),
		testSpec("gamma", newTestUpstream(t, "gamma", addEchoTool("fetch")), func(spec *ServerSpec) {
			spec.Enabled = false
		}),
	)
	service.virtualSpecs = map[string]VirtualServerSpec{
		"coding": {Name: "coding", Servers: []string{"alpha", "dead", "ghost"}},
		// A server keeps the name, so this virtual server is not served.
		"beta": {Name: "beta", Servers: []string{"alpha"}},
	}
	return service
}

func TestToolCatalogAggregatesAcrossServers(t *testing.T) {
	catalog, err := newToolCatalogTestService(t).ToolCatalog(ToolCatalogFilter{})
	require.NoError(t, err)

	alpha := ToolCatalogServer{
		Server: "alpha", ServerName: "Alpha Tools", Transport: config.MCPTransportHTTP, Enabled: true,
		Status: StatusConnected, UserPaths: []string{}, DisallowedUserPaths: []string{},
	}
	beta := ToolCatalogServer{
		Server: "beta", ServerName: "beta", Transport: config.MCPTransportHTTP, Enabled: true,
		Status: StatusConnected, UserPaths: []string{"/team/b"}, DisallowedUserPaths: []string{},
	}
	assert.Equal(t, []ToolCatalogServer{
		alpha,
		beta,
		// An unreachable server is reported with its status and no tools.
		{
			Server: "dead", ServerName: "dead", Transport: config.MCPTransportHTTP, Enabled: true,
			Status: StatusDegraded, UserPaths: []string{}, DisallowedUserPaths: []string{"/team/b"},
		},
		{
			Server: "gamma", ServerName: "gamma", Transport: config.MCPTransportHTTP,
			Status: StatusDisabled, UserPaths: []string{}, DisallowedUserPaths: []string{},
		},
	}, catalog.Servers)
	assert.Equal(t, []ToolCatalogTool{
		{ToolCatalogServer: alpha, Name: "alpha_drop", UpstreamName: "drop", Description: "drops a table", Destructive: true, Exposed: true},
		{ToolCatalogServer: alpha, Name: "alpha_echo", UpstreamName: "echo", Description: "echoes its input", Exposed: true},
		{ToolCatalogServer: alpha, Name: "alpha_read", UpstreamName: "read", Description: "Read a file", ReadOnly: true, Exposed: true},
		// Hidden by disallowed_tools: no client-facing name.
		{ToolCatalogServer: alpha, UpstreamName: "wipe", Description: "echoes its input"},
		{ToolCatalogServer: beta, Name: "beta_search", UpstreamName: "search", Description: "echoes its input", Exposed: true},
	}, catalog.Tools)
}

func TestToolCatalogFilters(t *testing.T) {
	service := newToolCatalogTestService(t)

	tests := []struct {
		name        string
		filter      ToolCatalogFilter
		wantServers []string
		wantTools   []string
		wantErr     bool
	}{
		{
			name:        "one server",
			filter:      ToolCatalogFilter{Server: "beta"},
			wantServers: []string{"beta"},
			wantTools:   []string{"search"},
		},
		{
			name:        "virtual server members, missing member skipped",
			filter:      ToolCatalogFilter{VirtualServer: "coding"},
			wantServers: []string{"alpha", "dead"},
			wantTools:   []string{"drop", "echo", "read", "wipe"},
		},
		{
			name:        "as seen by a path inside one scope and outside another",
			filter:      ToolCatalogFilter{UserPath: "/team/b/svc"},
			wantServers: []string{"alpha", "beta", "gamma"},
			wantTools:   []string{"drop", "echo", "read", "wipe", "search"},
		},
		{
			name:        "as seen by a path outside the scoped server",
			filter:      ToolCatalogFilter{UserPath: "/team/a"},
			wantServers: []string{"alpha", "dead", "gamma"},
			wantTools:   []string{"drop", "echo", "read", "wipe"},
		},
		{
			name:        "filters combine",
			filter:      ToolCatalogFilter{VirtualServer: "coding", UserPath: "/team/b"},
			wantServers: []string{"alpha"},
			wantTools:   []string{"drop", "echo", "read", "wipe"},
		},
		{
			name:        "a server outside the virtual server leaves nothing",
			filter:      ToolCatalogFilter{Server: "beta", VirtualServer: "coding"},
			wantServers: []string{},
			wantTools:   []string{},
		},
		{name: "unknown server", filter: ToolCatalogFilter{Server: "ghost"}, wantErr: true},
		{name: "unknown virtual server", filter: ToolCatalogFilter{VirtualServer: "ghost"}, wantErr: true},
		{name: "virtual server that is not served", filter: ToolCatalogFilter{VirtualServer: "beta"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			catalog, err := service.ToolCatalog(tt.filter)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrNotFound)
				return
			}
			require.NoError(t, err)
			servers := make([]string, 0, len(catalog.Servers))
			for _, server := range catalog.Servers {
				servers = append(servers, server.Server)
			}
			tools := make([]string, 0, len(catalog.Tools))
			for _, tool := range catalog.Tools {
				tools = append(tools, tool.UpstreamName)
			}
			assert.Equal(t, tt.wantServers, servers)
			assert.Equal(t, tt.wantTools, tools)
		})
	}
}

// A degraded server keeps its last catalog and clients still see those tools,
// so the inventory lists them next to the degraded status.
func TestToolCatalogKeepsToolsOfDegradedServer(t *testing.T) {
	upstream := mcp.NewServer(&mcp.Implementation{Name: "alpha", Version: "test"}, nil)
	addEchoTool("echo")(upstream)
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return upstream }, nil))
	service, _ := newTestService(t, nil, testSpec("alpha", ts.URL, nil))

	ts.CloseClientConnections()
	ts.Close()
	_, err := service.Reconnect(t.Context(), "alpha")
	require.Error(t, err)

	catalog, err := service.ToolCatalog(ToolCatalogFilter{})
	require.NoError(t, err)
	require.Len(t, catalog.Servers, 1)
	assert.Equal(t, StatusDegraded, catalog.Servers[0].Status)
	require.Len(t, catalog.Tools, 1)
	assert.Equal(t, "alpha_echo", catalog.Tools[0].Name)
}
