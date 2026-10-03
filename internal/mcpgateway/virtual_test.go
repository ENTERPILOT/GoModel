package mcpgateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
)

// newVirtualTestService serves alpha, beta, and gamma upstreams plus the
// given virtual servers.
func newVirtualTestService(t *testing.T, mutateBeta func(*ServerSpec), virtuals ...VirtualServerSpec) (*Service, string) {
	t.Helper()
	service, gatewayURL := newTestService(t, nil,
		testSpec("alpha", newTestUpstream(t, "alpha", addEchoTool("echo")), nil),
		testSpec("beta", newTestUpstream(t, "beta", addEchoTool("search")), mutateBeta),
		testSpec("gamma", newTestUpstream(t, "gamma", addEchoTool("fetch")), nil),
	)
	service.virtualSpecs = make(map[string]VirtualServerSpec, len(virtuals))
	for _, virtual := range virtuals {
		service.virtualSpecs[virtual.Name] = virtual
	}
	return service, gatewayURL
}

func TestVirtualServerServesOnlyMembers(t *testing.T) {
	_, gatewayURL := newVirtualTestService(t, nil,
		VirtualServerSpec{Name: "coding", Servers: []string{"alpha", "beta", "ghost"}})

	session := connectClient(t, gatewayURL+"/mcp/coding", nil)
	assert.Equal(t, []string{"alpha_echo", "beta_search"}, listToolNames(t, session), "members only, namespaced; a missing member is skipped")
	assert.Contains(t, session.InitializeResult().Instructions, `virtual server "coding" aggregating 2 server(s): alpha, beta`)

	for _, name := range []string{"beta_search", "search"} {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"q": 1}})
		require.NoError(t, err)
		require.False(t, result.IsError)
		text, ok := result.Content[0].(*mcp.TextContent)
		require.True(t, ok)
		assert.Equal(t, `echo:{"q":1}`, text.Text, "namespaced and unique bare names both relay")
	}

	_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "gamma_fetch"})
	require.Error(t, err, "a non-member tool is not callable")
}

func TestVirtualServerNeverWidensAccess(t *testing.T) {
	_, gatewayURL := newVirtualTestService(t, func(spec *ServerSpec) {
		spec.UserPaths = []string{"/team-b"}
		spec.AllowedTools = []string{"other"}
	}, VirtualServerSpec{Name: "coding", Servers: []string{"alpha", "beta"}})

	teamA := connectClient(t, gatewayURL+"/mcp/coding", map[string]string{core.UserPathHeader: "/team-a"})
	assert.Equal(t, []string{"alpha_echo"}, listToolNames(t, teamA), "a member outside the caller's user path stays hidden")

	teamB := connectClient(t, gatewayURL+"/mcp/coding", map[string]string{core.UserPathHeader: "/team-b"})
	assert.Equal(t, []string{"alpha_echo"}, listToolNames(t, teamB), "the member's tool filters still apply")

	narrowed := connectClient(t, gatewayURL+"/mcp/coding", map[string]string{ScopeHeader: "gamma"})
	assert.Empty(t, listToolNames(t, narrowed), "the scope header narrows the view and cannot add a non-member")
	assert.Contains(t, narrowed.InitializeResult().Instructions, "None of its member servers is available for this API key.")
}

func TestVirtualServerToolDiscoveryDefault(t *testing.T) {
	service, gatewayURL := newVirtualTestService(t, nil,
		VirtualServerSpec{Name: "search", Servers: []string{"alpha"}, ToolDiscovery: config.MCPToolDiscoverySearch},
		VirtualServerSpec{Name: "listed", Servers: []string{"alpha"}, ToolDiscovery: config.MCPToolDiscoveryOff},
		VirtualServerSpec{Name: "inherit", Servers: []string{"alpha"}},
	)
	service.searchDiscovery = true

	tests := []struct {
		endpoint string
		header   string
		want     []string
	}{
		{endpoint: "search", want: []string{callToolName, searchToolsName}},
		{endpoint: "listed", want: []string{"alpha_echo"}},
		{endpoint: "inherit", want: []string{callToolName, searchToolsName}},
		{endpoint: "search", header: config.MCPToolDiscoveryOff, want: []string{"alpha_echo"}},
	}
	for _, tt := range tests {
		t.Run(tt.endpoint+"/"+tt.header, func(t *testing.T) {
			headers := map[string]string{}
			if tt.header != "" {
				headers[ToolDiscoveryHeader] = tt.header
			}
			session := connectClient(t, gatewayURL+"/mcp/"+tt.endpoint, headers)
			assert.Equal(t, tt.want, listToolNames(t, session))
		})
	}
}

func TestVirtualServerYieldsToServerWithSameName(t *testing.T) {
	// Started before the service so cleanup closes the service first.
	codingURL := newTestUpstream(t, "coding", addEchoTool("lint"))
	service, gatewayURL := newVirtualTestService(t, nil,
		VirtualServerSpec{Name: "coding", Servers: []string{"alpha"}})

	// An admin-managed server claims the name after the virtual server exists.
	specs := make([]ServerSpec, 0, 4)
	for _, view := range service.Views() {
		specs = append(specs, view.Spec)
	}
	specs = append(specs, testSpec("coding", codingURL, func(spec *ServerSpec) {
		spec.Managed = false
	}))
	service.manager.Apply(specs)
	waitForConnected(t, service, len(specs))

	session := connectClient(t, gatewayURL+"/mcp/coding", nil)
	assert.Equal(t, []string{"lint"}, listToolNames(t, session), "the real server keeps its endpoint")

	views := service.VirtualViews()
	require.Len(t, views, 1)
	assert.Equal(t, `virtual MCP server "coding" is not served: MCP server "coding" uses the same name; rename one of them`, views[0].Conflict)
}

func TestVirtualViewsReportMembersAndDiscovery(t *testing.T) {
	service, _ := newVirtualTestService(t, nil,
		VirtualServerSpec{Name: "coding", Servers: []string{"alpha", "ghost"}, ToolDiscovery: config.MCPToolDiscoverySearch},
		VirtualServerSpec{Name: "all", Servers: []string{"alpha", "beta", "gamma"}},
	)

	views := service.VirtualViews()
	require.Len(t, views, 2)
	assert.Equal(t, "all", views[0].Spec.Name, "sorted by name")
	assert.Equal(t, config.MCPToolDiscoveryOff, views[0].ToolDiscovery)
	assert.Empty(t, views[0].MissingServers)
	assert.Empty(t, views[0].Conflict)
	assert.Equal(t, config.MCPToolDiscoverySearch, views[1].ToolDiscovery)
	assert.Equal(t, []string{"ghost"}, views[1].MissingServers)
}

func TestUnknownEndpointErrorNamesBothKinds(t *testing.T) {
	_, gatewayURL := newVirtualTestService(t, nil)

	resp, err := http.Post(gatewayURL+"/mcp/ghost", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Contains(t, string(body), "unknown MCP server or virtual server: ghost")
}

func TestSessionBindingRejectsSwitchBetweenVirtualAndPinned(t *testing.T) {
	_, gatewayURL := newVirtualTestService(t, nil,
		VirtualServerSpec{Name: "coding", Servers: []string{"alpha"}})

	listBody := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	virtualSession := initializeRawSession(t, gatewayURL+"/mcp/coding", nil)
	assert.Equal(t, http.StatusNotFound, rawMCPStatus(t, gatewayURL+"/mcp/alpha", listBody, map[string]string{"Mcp-Session-Id": virtualSession}))
	assert.Equal(t, http.StatusNotFound, rawMCPStatus(t, gatewayURL+"/mcp", listBody, map[string]string{"Mcp-Session-Id": virtualSession}))

	pinnedSession := initializeRawSession(t, gatewayURL+"/mcp/alpha", nil)
	assert.Equal(t, http.StatusNotFound, rawMCPStatus(t, gatewayURL+"/mcp/coding", listBody, map[string]string{"Mcp-Session-Id": pinnedSession}))
}

// memoryStore is a minimal in-process Store for admin write paths.
type memoryStore struct{ rows map[string]ManagedServer }

func (m *memoryStore) List(context.Context) ([]ManagedServer, error) {
	rows := make([]ManagedServer, 0, len(m.rows))
	for _, row := range m.rows {
		rows = append(rows, row)
	}
	return rows, nil
}

func (m *memoryStore) Get(_ context.Context, name string) (*ManagedServer, error) {
	row, ok := m.rows[name]
	if !ok {
		return nil, ErrNotFound
	}
	return &row, nil
}

func (m *memoryStore) Upsert(_ context.Context, server ManagedServer) error {
	m.rows[server.Name] = server
	return nil
}

func (m *memoryStore) Delete(_ context.Context, name string) error {
	delete(m.rows, name)
	return nil
}

func (m *memoryStore) Close() error { return nil }

func TestUpsertRejectsVirtualServerName(t *testing.T) {
	store := &memoryStore{rows: map[string]ManagedServer{}}
	service, err := NewService(context.Background(), Options{
		Store:          store,
		VirtualServers: map[string]VirtualServerSpec{"coding": {Name: "coding", Servers: []string{"alpha"}}},
	})
	require.NoError(t, err)
	t.Cleanup(service.Close)

	err = service.Upsert(context.Background(), ManagedServer{Name: "coding", URL: "https://example.com/mcp", Transport: config.MCPTransportHTTP})
	require.Error(t, err)
	assert.Equal(t, `slug "coding" is used by virtual MCP server "coding" (declared in config); choose another slug`, err.Error())
	assert.Empty(t, store.rows, "nothing is persisted")
}
