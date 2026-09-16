package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeMCPConfigDefaultsAndValidation(t *testing.T) {
	cfg := MCPConfig{Servers: map[string]MCPServerConfig{
		"GitHub": {URL: "https://api.githubcopilot.com/mcp"},
		"local":  {Command: "npx", Args: []string{"-y", "some-server"}},
	}}
	err := normalizeMCPConfig(&cfg)
	require.NoError(t, err)

	github, ok := cfg.Servers["github"]
	require.True(t, ok, "server name not canonicalized to lowercase: %v", cfg.Servers)
	require.Equal(t, MCPTransportHTTP, github.Transport)
	require.Equal(t, DefaultMCPToolTimeout, github.ToolTimeout)

	local := cfg.Servers["local"]
	require.Equal(t, MCPTransportStdio, local.Transport)
}

func TestMCPServerDisplayNameAndSlug(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"Linear MCP", "线性 MCP", "Линейный сервер", "MCP 🚀"} {
		err := ValidateMCPServerName(name)
		assert.NoError(t, err)
	}
	err := ValidateMCPServerSlug("linear-mcp")
	require.NoError(t, err)
	require.Error(t, ValidateMCPServerSlug("Linear MCP"))

	derived := map[string]string{
		"Linear MCP": "linear-mcp",
		"Café Tools": "cafe-tools",
		"线性":         "mcp-b7ccbb8b",
	}
	for name, want := range derived {
		got := DeriveMCPServerSlug(name)
		assert.Equal(t, want, got)
	}
}

func TestNormalizeMCPConfigRejectsInvalid(t *testing.T) {
	tests := []struct {
		name    string
		servers map[string]MCPServerConfig
		wantErr string
	}{
		{
			name:    "http without url",
			servers: map[string]MCPServerConfig{"a": {Transport: "http"}},
			wantErr: "url is required",
		},
		{
			name:    "stdio without command",
			servers: map[string]MCPServerConfig{"a": {Transport: "stdio"}},
			wantErr: "command is required",
		},
		{
			name:    "url and command conflict",
			servers: map[string]MCPServerConfig{"a": {URL: "https://x/mcp", Command: "npx"}},
			wantErr: "command is only valid",
		},
		{
			name:    "bad scheme",
			servers: map[string]MCPServerConfig{"a": {URL: "ftp://x"}},
			wantErr: "http:// or https://",
		},
		{
			name:    "bad transport",
			servers: map[string]MCPServerConfig{"a": {URL: "https://x/mcp", Transport: "websocket"}},
			wantErr: "transport must be one of",
		},
		{
			name:    "bad name",
			servers: map[string]MCPServerConfig{"Bad Name!": {URL: "https://x/mcp"}},
			wantErr: "must match",
		},
		{
			name:    "negative timeout",
			servers: map[string]MCPServerConfig{"a": {URL: "https://x/mcp", ToolTimeout: -time.Second}},
			wantErr: "tool_timeout",
		},
		{
			name:    "stdio with headers",
			servers: map[string]MCPServerConfig{"a": {Command: "npx", Headers: map[string]string{"X": "y"}}},
			wantErr: "headers are only valid",
		},
		{
			name:    "invalid user path",
			servers: map[string]MCPServerConfig{"a": {URL: "https://x/mcp", UserPaths: []string{"/team/../admin"}}},
			wantErr: "invalid user_paths",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := MCPConfig{Servers: tt.servers}
			err := normalizeMCPConfig(&cfg)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestNormalizeMCPConfigCanonicalizesUserPaths(t *testing.T) {
	cfg := MCPConfig{Servers: map[string]MCPServerConfig{
		"a": {
			URL:       "https://x/mcp",
			UserPaths: []string{" team/a ", "/team/a", "/team/b/"},
		},
	}}
	err := normalizeMCPConfig(&cfg)
	require.NoError(t, err)

	got := cfg.Servers["a"].UserPaths
	want := []string{"/team/a", "/team/b"}
	require.Equal(t, want, got)
}

func TestApplyMCPEnvMergesOverYAML(t *testing.T) {
	cfg := &Config{MCP: MCPConfig{
		Enabled: true,
		Servers: map[string]MCPServerConfig{
			"github": {URL: "https://yaml.example/mcp"},
			"other":  {URL: "https://other.example/mcp"},
		},
	}}
	t.Setenv("MCP_SERVERS", `{"github":{"url":"https://env.example/mcp","transport":"sse"},"extra":{"url":"https://extra.example/mcp"}}`)
	err := applyMCPEnv(cfg)
	require.NoError(t, err)
	err = normalizeMCPConfig(&cfg.MCP)
	require.NoError(t, err)
	require.Len(t, cfg.MCP.Servers, 3)

	github := cfg.MCP.Servers["github"]
	require.Equal(t, "https://env.example/mcp", github.URL)
	require.Equal(t, MCPTransportSSE, github.Transport, "env entry did not replace YAML entry: %+v", github)
	require.Equal(t, "https://other.example/mcp", cfg.MCP.Servers["other"].URL, "untouched YAML entry lost: %+v", cfg.MCP.Servers["other"])
	require.Equal(t, "https://extra.example/mcp", cfg.MCP.Servers["extra"].URL, "env-only entry missing: %+v", cfg.MCP.Servers["extra"])
}

func TestApplyMCPEnvExpandsEnvironmentReferences(t *testing.T) {
	t.Setenv("MCP_TEST_TOKEN", `secret"token\value`)
	t.Setenv("MCP_SERVERS", `{"github":{"url":"https://example.com/mcp","headers":{"Authorization":"Bearer ${MCP_TEST_TOKEN}"}}}`)
	cfg := &Config{}
	err := applyMCPEnv(cfg)
	require.NoError(t, err)

	got := cfg.MCP.Servers["github"].Headers["Authorization"]
	require.Equal(t, `Bearer secret"token\value`, got)
}

func TestApplyMCPEnvRejectsInvalidJSON(t *testing.T) {
	cfg := &Config{}
	t.Setenv("MCP_SERVERS", `[not json`)
	require.Error(t, applyMCPEnv(cfg))
}

func TestApplyMCPEnvRejectsCanonicalNameCollision(t *testing.T) {
	cfg := &Config{}
	t.Setenv("MCP_SERVERS", `{"GitHub":{"url":"https://a.example/mcp"},"github":{"url":"https://b.example/mcp"}}`)
	err := applyMCPEnv(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "canonicalize")
}

func TestMCPServerEnabledDefaultsTrue(t *testing.T) {
	require.True(t, MCPServerEnabled(MCPServerConfig{}))

	off := false
	require.False(t, MCPServerEnabled(MCPServerConfig{Enabled: &off}))
}

func TestNormalizeMCPAllowedOrigins(t *testing.T) {
	tests := []struct {
		name    string
		input   []string
		want    []string
		wantErr bool
	}{
		{
			name:  "canonicalizes case and drops blanks and duplicates",
			input: []string{"HTTPS://Console.Example.com", "  ", "https://console.example.com"},
			want:  []string{"https://console.example.com"},
		},
		{
			name:  "preserves the port and the wildcard",
			input: []string{"http://localhost:3000", "*"},
			want:  []string{"http://localhost:3000", "*"},
		},
		{
			// Browsers serialize an origin without its default port, so an
			// explicitly configured one has to canonicalize away or it would
			// silently never match.
			name:  "drops a default port so it matches what a browser sends",
			input: []string{"https://console.example.com:443", "http://intranet.example:80"},
			want:  []string{"https://console.example.com", "http://intranet.example"},
		},
		{
			name:  "collapses an origin written both with and without its default port",
			input: []string{"https://console.example.com:443", "https://console.example.com"},
			want:  []string{"https://console.example.com"},
		},
		{
			name:  "keeps a port that is not the default for the scheme",
			input: []string{"https://console.example.com:80", "http://console.example.com:443"},
			want:  []string{"https://console.example.com:80", "http://console.example.com:443"},
		},
		{
			name:  "drops a default port from a bracketed IPv6 host",
			input: []string{"https://[::1]:443"},
			want:  []string{"https://[::1]"},
		},
		{
			name:    "rejects an origin with no scheme",
			input:   []string{"console.example.com"},
			wantErr: true,
		},
		{
			name:    "rejects an origin carrying a path",
			input:   []string{"https://console.example.com/mcp"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &MCPConfig{AllowedOrigins: tt.input}
			err := normalizeMCPConfig(cfg)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, cfg.AllowedOrigins)
		})
	}
}
