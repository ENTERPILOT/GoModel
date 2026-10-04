package mcpgateway

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
)

// newVirtualAdminTestService serves alpha and beta upstreams from config,
// with in-memory stores for servers and virtual servers.
func newVirtualAdminTestService(t *testing.T, configVirtuals map[string]VirtualServerSpec, stored ...ManagedVirtualServer) (*Service, *memoryVirtualStore, string) {
	t.Helper()
	virtualStore := &memoryVirtualStore{rows: map[string]ManagedVirtualServer{}}
	for _, row := range stored {
		virtualStore.rows[row.Name] = row
	}
	service, gatewayURL := newTestServiceWithOptions(t, Options{
		ConfigServers: map[string]ServerSpec{
			"alpha": testSpec("alpha", newTestUpstream(t, "alpha", addEchoTool("echo")), nil),
			"beta":  testSpec("beta", newTestUpstream(t, "beta", addEchoTool("search")), nil),
		},
		Store:          &memoryStore{rows: map[string]ManagedServer{}},
		VirtualServers: configVirtuals,
		VirtualStore:   virtualStore,
	})
	return service, virtualStore, gatewayURL
}

func TestUpsertVirtualServesImmediately(t *testing.T) {
	service, virtualStore, gatewayURL := newVirtualAdminTestService(t, nil)

	err := service.UpsertVirtual(context.Background(), ManagedVirtualServer{
		Name:          " Coding ",
		Description:   "code tools",
		Servers:       []string{"beta", "alpha"},
		ToolDiscovery: config.MCPToolDiscoverySearch,
	})
	require.NoError(t, err)
	require.Contains(t, virtualStore.rows, "coding", "the name is normalized before it is stored")

	views := service.VirtualViews()
	require.Len(t, views, 1)
	assert.False(t, views[0].Spec.Managed)
	assert.Equal(t, []string{"beta", "alpha"}, views[0].Spec.Servers)

	session := connectClient(t, gatewayURL+"/mcp/coding", map[string]string{ToolDiscoveryHeader: config.MCPToolDiscoveryOff})
	assert.Equal(t, []string{"alpha_echo", "beta_search"}, listToolNames(t, session))
}

func TestUpsertVirtualRejections(t *testing.T) {
	tests := []struct {
		name    string
		virtual ManagedVirtualServer
		wantErr string
	}{
		{
			name:    "unknown member",
			virtual: ManagedVirtualServer{Name: "coding", Servers: []string{"alpha", "ghost"}},
			wantErr: `member "ghost" matches no MCP server`,
		},
		{
			name:    "nested virtual server",
			virtual: ManagedVirtualServer{Name: "all", Servers: []string{"research"}},
			wantErr: `member "research" is a virtual server; virtual servers can only include MCP servers`,
		},
		{
			name:    "server slug",
			virtual: ManagedVirtualServer{Name: "alpha", Servers: []string{"beta"}},
			wantErr: `name "alpha" is used by MCP server "alpha"; choose another name`,
		},
		{
			name:    "config-declared name",
			virtual: ManagedVirtualServer{Name: "research", Servers: []string{"beta"}},
			wantErr: `virtual MCP server "research" is managed by config/env and is read-only`,
		},
		{
			name:    "invalid name",
			virtual: ManagedVirtualServer{Name: "my tools", Servers: []string{"alpha"}},
			wantErr: `virtual server name "my tools" must match`,
		},
		{
			name:    "no members",
			virtual: ManagedVirtualServer{Name: "coding"},
			wantErr: "servers is required",
		},
		{
			name:    "invalid tool discovery",
			virtual: ManagedVirtualServer{Name: "coding", Servers: []string{"alpha"}, ToolDiscovery: "semantic"},
			wantErr: `tool_discovery must be "off" or "search"`,
		},
	}
	service, virtualStore, _ := newVirtualAdminTestService(t, map[string]VirtualServerSpec{
		"research": {Name: "research", Servers: []string{"beta"}, Managed: true},
	})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := service.UpsertVirtual(context.Background(), tt.virtual)
			require.ErrorIs(t, err, ErrInvalidVirtualServer)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Empty(t, virtualStore.rows)
		})
	}
}

func TestUpsertVirtualKeepsRowStoredBeforeServerEditable(t *testing.T) {
	// The row predates the "alpha" server, so it is not served but stays editable.
	service, virtualStore, _ := newVirtualAdminTestService(t, nil,
		ManagedVirtualServer{Name: "alpha", Servers: []string{"beta"}})
	require.NotEmpty(t, service.VirtualViews()[0].Conflict)

	err := service.UpsertVirtual(context.Background(), ManagedVirtualServer{Name: "alpha", Description: "renamed soon", Servers: []string{"beta"}})
	require.NoError(t, err)
	assert.Equal(t, "renamed soon", virtualStore.rows["alpha"].Description)

	// If the row is deleted while the edit is in flight, the edit must not
	// recreate it under the server's slug.
	delete(virtualStore.rows, "alpha")
	err = service.UpsertVirtual(context.Background(), ManagedVirtualServer{Name: "alpha", Servers: []string{"beta"}})
	require.ErrorIs(t, err, ErrInvalidVirtualServer)
	assert.Empty(t, virtualStore.rows)
}

func TestConfigVirtualServerShadowsStoredRow(t *testing.T) {
	service, _, _ := newVirtualAdminTestService(t,
		map[string]VirtualServerSpec{"coding": {Name: "coding", Servers: []string{"alpha"}, Managed: true}},
		ManagedVirtualServer{Name: "coding", Servers: []string{"beta"}},
	)

	views := service.VirtualViews()
	require.Len(t, views, 1)
	assert.True(t, views[0].Spec.Managed)
	assert.Equal(t, []string{"alpha"}, views[0].Spec.Servers)
	assert.True(t, service.IsManagedVirtual("coding"))
}

func TestDeleteVirtualEndsItsEndpoint(t *testing.T) {
	service, virtualStore, gatewayURL := newVirtualAdminTestService(t,
		map[string]VirtualServerSpec{"research": {Name: "research", Servers: []string{"beta"}, Managed: true}},
		ManagedVirtualServer{Name: "coding", Servers: []string{"alpha"}},
	)
	sessionID := initializeRawSession(t, gatewayURL+"/mcp/coding", nil)

	require.NoError(t, service.DeleteVirtual(context.Background(), "coding"))
	assert.Empty(t, virtualStore.rows)
	assert.False(t, service.IsVirtual("coding"))

	listBody := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	status := rawMCPStatus(t, gatewayURL+"/mcp/coding", listBody, map[string]string{"Mcp-Session-Id": sessionID})
	assert.Equal(t, http.StatusNotFound, status, "a session on a deleted virtual server ends")

	require.ErrorIs(t, service.DeleteVirtual(context.Background(), "coding"), ErrNotFound)
	err := service.DeleteVirtual(context.Background(), "research")
	require.ErrorIs(t, err, ErrInvalidVirtualServer, "config-declared virtual servers are read-only")
}

func TestUpsertServerRejectsStoredVirtualName(t *testing.T) {
	service, _, _ := newVirtualAdminTestService(t, nil,
		ManagedVirtualServer{Name: "coding", Servers: []string{"alpha"}})

	err := service.Upsert(context.Background(), ManagedServer{Name: "coding", URL: "https://example.com/mcp", Transport: config.MCPTransportHTTP})
	require.ErrorIs(t, err, ErrVirtualNameTaken)
	assert.Equal(t, `slug "coding" is used by virtual MCP server "coding"; choose another slug`, err.Error())
}
