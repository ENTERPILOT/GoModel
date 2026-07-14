package config

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestNormalizeMCPConfigDefaultsAndValidation(t *testing.T) {
	cfg := MCPConfig{Servers: map[string]MCPServerConfig{
		"GitHub": {URL: "https://api.githubcopilot.com/mcp"},
		"local":  {Command: "npx", Args: []string{"-y", "some-server"}},
	}}
	if err := normalizeMCPConfig(&cfg); err != nil {
		t.Fatalf("normalizeMCPConfig() error = %v", err)
	}

	github, ok := cfg.Servers["github"]
	if !ok {
		t.Fatalf("server name not canonicalized to lowercase: %v", cfg.Servers)
	}
	if github.Transport != MCPTransportHTTP {
		t.Fatalf("default transport = %q, want http", github.Transport)
	}
	if github.ToolTimeout != DefaultMCPToolTimeout {
		t.Fatalf("default tool timeout = %v, want %v", github.ToolTimeout, DefaultMCPToolTimeout)
	}

	local := cfg.Servers["local"]
	if local.Transport != MCPTransportStdio {
		t.Fatalf("command-only server transport = %q, want stdio inferred", local.Transport)
	}
}

func TestMCPServerDisplayNameAndSlug(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"Linear MCP", "线性 MCP", "Линейный сервер", "MCP 🚀"} {
		if err := ValidateMCPServerName(name); err != nil {
			t.Errorf("ValidateMCPServerName(%q) error = %v", name, err)
		}
	}
	if err := ValidateMCPServerSlug("linear-mcp"); err != nil {
		t.Fatalf("ValidateMCPServerSlug(valid) error = %v", err)
	}
	if err := ValidateMCPServerSlug("Linear MCP"); err == nil {
		t.Fatal("ValidateMCPServerSlug should reject spaces and uppercase characters")
	}

	derived := map[string]string{
		"Linear MCP": "linear-mcp",
		"Café Tools": "cafe-tools",
		"线性":         "mcp-b7ccbb8b",
	}
	for name, want := range derived {
		if got := DeriveMCPServerSlug(name); got != want {
			t.Errorf("DeriveMCPServerSlug(%q) = %q, want %q", name, got, want)
		}
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
			if err == nil {
				t.Fatalf("normalizeMCPConfig() = nil, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("normalizeMCPConfig() error = %v, want containing %q", err, tt.wantErr)
			}
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
	if err := normalizeMCPConfig(&cfg); err != nil {
		t.Fatalf("normalizeMCPConfig() error = %v", err)
	}
	got := cfg.Servers["a"].UserPaths
	want := []string{"/team/a", "/team/b"}
	if !slices.Equal(got, want) {
		t.Fatalf("UserPaths = %v, want %v", got, want)
	}
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

	if err := applyMCPEnv(cfg); err != nil {
		t.Fatalf("applyMCPEnv() error = %v", err)
	}
	if err := normalizeMCPConfig(&cfg.MCP); err != nil {
		t.Fatalf("normalizeMCPConfig() error = %v", err)
	}

	if len(cfg.MCP.Servers) != 3 {
		t.Fatalf("len(Servers) = %d, want 3", len(cfg.MCP.Servers))
	}
	github := cfg.MCP.Servers["github"]
	if github.URL != "https://env.example/mcp" || github.Transport != MCPTransportSSE {
		t.Fatalf("env entry did not replace YAML entry: %+v", github)
	}
	if cfg.MCP.Servers["other"].URL != "https://other.example/mcp" {
		t.Fatalf("untouched YAML entry lost: %+v", cfg.MCP.Servers["other"])
	}
	if cfg.MCP.Servers["extra"].URL != "https://extra.example/mcp" {
		t.Fatalf("env-only entry missing: %+v", cfg.MCP.Servers["extra"])
	}
}

func TestApplyMCPEnvExpandsEnvironmentReferences(t *testing.T) {
	t.Setenv("MCP_TEST_TOKEN", `secret"token\value`)
	t.Setenv("MCP_SERVERS", `{"github":{"url":"https://example.com/mcp","headers":{"Authorization":"Bearer ${MCP_TEST_TOKEN}"}}}`)
	cfg := &Config{}

	if err := applyMCPEnv(cfg); err != nil {
		t.Fatalf("applyMCPEnv() error = %v", err)
	}

	got := cfg.MCP.Servers["github"].Headers["Authorization"]
	if got != `Bearer secret"token\value` {
		t.Fatalf("Authorization header = %q, want expanded token", got)
	}
}

func TestApplyMCPEnvRejectsInvalidJSON(t *testing.T) {
	cfg := &Config{}
	t.Setenv("MCP_SERVERS", `[not json`)
	if err := applyMCPEnv(cfg); err == nil {
		t.Fatalf("applyMCPEnv() with invalid JSON should fail")
	}
}

func TestApplyMCPEnvRejectsCanonicalNameCollision(t *testing.T) {
	cfg := &Config{}
	t.Setenv("MCP_SERVERS", `{"GitHub":{"url":"https://a.example/mcp"},"github":{"url":"https://b.example/mcp"}}`)
	err := applyMCPEnv(cfg)
	if err == nil {
		t.Fatalf("applyMCPEnv() with colliding canonical names should fail")
	}
	if !strings.Contains(err.Error(), "canonicalize") {
		t.Fatalf("applyMCPEnv() error = %v, want canonical-collision message", err)
	}
}

func TestMCPServerEnabledDefaultsTrue(t *testing.T) {
	if !MCPServerEnabled(MCPServerConfig{}) {
		t.Fatalf("MCPServerEnabled(zero) = false, want true")
	}
	off := false
	if MCPServerEnabled(MCPServerConfig{Enabled: &off}) {
		t.Fatalf("MCPServerEnabled(disabled) = true, want false")
	}
}
