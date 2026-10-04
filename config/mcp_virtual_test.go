package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeMCPVirtualServers(t *testing.T) {
	cfg := MCPConfig{
		Servers: map[string]MCPServerConfig{"github": {URL: "https://example.com/mcp"}},
		VirtualServers: map[string]MCPVirtualServerConfig{
			"Coding": {
				Description:   "  code tools ",
				Servers:       []string{"GitHub", " linear ", "github", ""},
				ToolDiscovery: "Search",
			},
		},
	}
	require.NoError(t, normalizeMCPConfig(&cfg))

	coding, ok := cfg.VirtualServers["coding"]
	require.True(t, ok, "virtual server name not canonicalized: %v", cfg.VirtualServers)
	assert.Equal(t, "code tools", coding.Description)
	assert.Equal(t, []string{"github", "linear"}, coding.Servers, "members are canonicalized and deduplicated; unknown ones resolve at runtime")
	assert.Equal(t, MCPToolDiscoverySearch, coding.ToolDiscovery)
}

func TestNormalizeMCPVirtualServersAcceptsMemberNamedLikeAVirtualServer(t *testing.T) {
	// A dashboard server may own the slug "coding", and a server wins
	// /mcp/{name} over a virtual server, so the member resolves at runtime.
	cfg := MCPConfig{VirtualServers: map[string]MCPVirtualServerConfig{
		"all":    {Servers: []string{"coding"}},
		"coding": {Servers: []string{"github"}},
	}}
	require.NoError(t, normalizeMCPConfig(&cfg))
	assert.Equal(t, []string{"coding"}, cfg.VirtualServers["all"].Servers)
}

func TestNormalizeMCPVirtualServersRejectsInvalid(t *testing.T) {
	tests := []struct {
		name     string
		servers  map[string]MCPServerConfig
		virtuals map[string]MCPVirtualServerConfig
		wantErr  string
	}{
		{
			name:     "clashes with a server slug",
			servers:  map[string]MCPServerConfig{"github": {URL: "https://example.com/mcp"}},
			virtuals: map[string]MCPVirtualServerConfig{"GitHub": {Servers: []string{"github"}}},
			wantErr:  `mcp.virtual_servers["github"]: name clashes with mcp.servers["github"]; virtual server names and server slugs share /mcp/{name}`,
		},
		{
			name:     "no members",
			virtuals: map[string]MCPVirtualServerConfig{"coding": {Servers: []string{" "}}},
			wantErr:  `mcp.virtual_servers["coding"]: servers is required: list at least one MCP server slug`,
		},
		{
			name:     "invalid name",
			virtuals: map[string]MCPVirtualServerConfig{"my tools": {Servers: []string{"github"}}},
			wantErr:  `mcp.virtual_servers["my tools"]: virtual server name "my tools" must match`,
		},
		{
			name:     "invalid member",
			virtuals: map[string]MCPVirtualServerConfig{"coding": {Servers: []string{"git hub"}}},
			wantErr:  `mcp.virtual_servers["coding"]: member "git hub": server slug "git hub" must match`,
		},
		{
			name:     "invalid tool discovery",
			virtuals: map[string]MCPVirtualServerConfig{"coding": {Servers: []string{"github"}, ToolDiscovery: "semantic"}},
			wantErr:  `mcp.virtual_servers["coding"]: tool_discovery must be "off" or "search", got "semantic"`,
		},
		{
			name: "names that canonicalize together",
			virtuals: map[string]MCPVirtualServerConfig{
				"Coding": {Servers: []string{"github"}},
				"coding": {Servers: []string{"github"}},
			},
			wantErr: `mcp.virtual_servers: duplicate virtual server name "coding"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := MCPConfig{Servers: tt.servers, VirtualServers: tt.virtuals}
			err := normalizeMCPConfig(&cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestApplyMCPVirtualEnvMergesOverYAML(t *testing.T) {
	t.Setenv("MCP_TEST_MEMBER", "linear")
	t.Setenv("MCP_VIRTUAL_SERVERS", `{"coding":{"servers":["github","${MCP_TEST_MEMBER}"]},"extra":{"servers":["sentry"]}}`)
	cfg := &Config{MCP: MCPConfig{VirtualServers: map[string]MCPVirtualServerConfig{
		"Coding":   {Servers: []string{"yaml-only"}},
		"research": {Servers: []string{"exa"}},
	}}}
	require.NoError(t, applyMCPVirtualEnv(cfg))
	require.NoError(t, normalizeMCPConfig(&cfg.MCP))

	assert.Equal(t, []string{"github", "linear"}, cfg.MCP.VirtualServers["coding"].Servers, "env entry replaces the YAML entry")
	assert.Equal(t, []string{"exa"}, cfg.MCP.VirtualServers["research"].Servers)
	assert.Equal(t, []string{"sentry"}, cfg.MCP.VirtualServers["extra"].Servers)
}

func TestApplyMCPVirtualEnvRejectsInvalid(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		wantErr string
	}{
		{name: "invalid json", env: `[not json`, wantErr: "invalid MCP_VIRTUAL_SERVERS"},
		{name: "canonical collision", env: `{"Coding":{"servers":["a"]},"coding":{"servers":["b"]}}`, wantErr: "canonicalize"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("MCP_VIRTUAL_SERVERS", tt.env)
			err := applyMCPVirtualEnv(&Config{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
