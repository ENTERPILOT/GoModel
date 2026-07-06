package config

import (
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

func TestApplyMCPEnvRejectsInvalidJSON(t *testing.T) {
	cfg := &Config{}
	t.Setenv("MCP_SERVERS", `[not json`)
	if err := applyMCPEnv(cfg); err == nil {
		t.Fatalf("applyMCPEnv() with invalid JSON should fail")
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
