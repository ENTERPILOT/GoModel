package config

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// DefaultMCPToolTimeout bounds a single upstream tools/call when the server
// declares no timeout of its own.
const DefaultMCPToolTimeout = 30 * time.Second

// MCP transport names accepted in MCPServerConfig.Transport.
const (
	MCPTransportHTTP  = "http"
	MCPTransportSSE   = "sse"
	MCPTransportStdio = "stdio"
)

// MCPConfig declares the MCP gateway: upstream MCP servers aggregated behind
// the authenticated /mcp endpoint. Declarative entries override admin-store
// rows with the same name and are read-only in the dashboard.
type MCPConfig struct {
	// Enabled gates the /mcp routes. Default: true (a no-op without servers).
	Enabled bool `yaml:"enabled" env:"MCP_ENABLED"`

	// Servers maps server names to upstream definitions. Names become tool
	// namespaces (aggregated tools are exposed as "{name}_{tool}") and URL
	// segments (/mcp/{name}), so they are restricted to [a-z0-9_-].
	Servers map[string]MCPServerConfig `yaml:"servers"`
}

// MCPServerConfig declares one upstream MCP server.
type MCPServerConfig struct {
	// URL is the upstream MCP endpoint for http/sse transports.
	URL string `yaml:"url,omitempty" json:"url,omitempty"`

	// Transport selects the upstream transport: "http" (streamable HTTP,
	// default), "sse" (legacy HTTP+SSE), or "stdio" (spawned subprocess).
	Transport string `yaml:"transport,omitempty" json:"transport,omitempty"`

	// Headers are sent verbatim on every upstream request (http/sse). Values
	// support ${ENV} expansion via the standard config pipeline. This is the
	// credential boundary: client bearer tokens are never forwarded upstream.
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`

	// Command, Args, and Env launch a stdio server as a subprocess. Stdio
	// servers are deliberately declarative-only: the admin API and dashboard
	// reject them, because registering subprocesses at runtime is a remote
	// code execution vector.
	Command string            `yaml:"command,omitempty" json:"command,omitempty"`
	Args    []string          `yaml:"args,omitempty" json:"args,omitempty"`
	Env     map[string]string `yaml:"env,omitempty" json:"env,omitempty"`

	// Description is an optional human-readable note.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`

	// Enabled toggles the entry. It defaults to true when omitted.
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`

	// AllowedTools restricts the tools exposed from this server (original,
	// un-prefixed names). Empty means all tools.
	AllowedTools []string `yaml:"allowed_tools,omitempty" json:"allowed_tools,omitempty"`

	// DisallowedTools hides specific tools; applied after AllowedTools.
	DisallowedTools []string `yaml:"disallowed_tools,omitempty" json:"disallowed_tools,omitempty"`

	// UserPaths scopes server visibility to specific request user paths
	// (subtree match, same semantics as virtual models). Empty means all.
	UserPaths []string `yaml:"user_paths,omitempty" json:"user_paths,omitempty"`

	// ToolTimeout bounds a single tools/call against this server.
	// Default: 30s.
	ToolTimeout time.Duration `yaml:"tool_timeout,omitempty" json:"tool_timeout,omitempty"`
}

const envMCPServers = "MCP_SERVERS"

// mcpServerNameRegex mirrors provider naming: lowercase alphanumerics with
// hyphens/underscores, starting with an alphanumeric. Names become tool-name
// prefixes, so the charset must stay inside the MCP tool-name alphabet.
var mcpServerNameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

const maxMCPServerNameLength = 64

// applyMCPEnv parses the MCP_SERVERS env var — a JSON object mapping server
// names to definitions — and merges it over the YAML-declared map. Env entries
// replace YAML entries with the same name, consistent with the rest of the
// config pipeline where env always wins.
func applyMCPEnv(cfg *Config) error {
	raw := strings.TrimSpace(os.Getenv(envMCPServers))
	if raw == "" {
		return nil
	}
	var fromEnv map[string]MCPServerConfig
	if err := json.Unmarshal([]byte(raw), &fromEnv); err != nil {
		return fmt.Errorf("invalid %s: %w", envMCPServers, err)
	}
	if len(fromEnv) == 0 {
		return nil
	}
	if cfg.MCP.Servers == nil {
		cfg.MCP.Servers = make(map[string]MCPServerConfig, len(fromEnv))
	}
	seen := make(map[string]string, len(fromEnv))
	for name, server := range fromEnv {
		canonical := canonicalTextKey(name)
		// Two JSON keys collapsing onto one canonical name would otherwise
		// pick a survivor by map iteration order — fail loudly instead.
		if previous, dup := seen[canonical]; dup {
			return fmt.Errorf("%s: entries %q and %q both canonicalize to server name %q", envMCPServers, previous, name, canonical)
		}
		seen[canonical] = name
		cfg.MCP.Servers[canonical] = server
	}
	return nil
}

// normalizeMCPConfig canonicalizes server names, applies defaults, and rejects
// invalid entries. It runs at load time so a bad declaration fails startup
// loudly instead of silently dropping the server.
func normalizeMCPConfig(cfg *MCPConfig) error {
	if len(cfg.Servers) == 0 {
		return nil
	}
	normalized := make(map[string]MCPServerConfig, len(cfg.Servers))
	for name, server := range cfg.Servers {
		canonical := canonicalTextKey(name)
		if err := ValidateMCPServerName(canonical); err != nil {
			return fmt.Errorf("mcp.servers[%q]: %w", name, err)
		}
		if _, dup := normalized[canonical]; dup {
			return fmt.Errorf("mcp.servers: duplicate server name %q", canonical)
		}
		if err := ValidateMCPServerConfig(&server); err != nil {
			return fmt.Errorf("mcp.servers[%q]: %w", canonical, err)
		}
		normalized[canonical] = server
	}
	cfg.Servers = normalized
	return nil
}

// ValidateMCPServerName rejects names that cannot serve as tool-name prefixes
// or URL segments.
func ValidateMCPServerName(name string) error {
	if name == "" {
		return fmt.Errorf("server name is required")
	}
	if len(name) > maxMCPServerNameLength {
		return fmt.Errorf("server name exceeds %d characters", maxMCPServerNameLength)
	}
	if !mcpServerNameRegex.MatchString(name) {
		return fmt.Errorf("server name %q must match %s", name, mcpServerNameRegex.String())
	}
	return nil
}

// ValidateMCPServerConfig validates one server definition and applies
// defaults in place. It is shared by config loading and the admin API.
func ValidateMCPServerConfig(server *MCPServerConfig) error {
	server.Transport = strings.ToLower(strings.TrimSpace(server.Transport))
	server.URL = strings.TrimSpace(server.URL)
	server.Command = strings.TrimSpace(server.Command)
	if server.Transport == "" {
		if server.Command != "" && server.URL == "" {
			server.Transport = MCPTransportStdio
		} else {
			server.Transport = MCPTransportHTTP
		}
	}
	switch server.Transport {
	case MCPTransportHTTP, MCPTransportSSE:
		if server.URL == "" {
			return fmt.Errorf("url is required for the %s transport", server.Transport)
		}
		if !strings.HasPrefix(server.URL, "http://") && !strings.HasPrefix(server.URL, "https://") {
			return fmt.Errorf("url must start with http:// or https://")
		}
		if server.Command != "" {
			return fmt.Errorf("command is only valid for the stdio transport")
		}
	case MCPTransportStdio:
		if server.Command == "" {
			return fmt.Errorf("command is required for the stdio transport")
		}
		if server.URL != "" {
			return fmt.Errorf("url is only valid for the http and sse transports")
		}
		if len(server.Headers) > 0 {
			return fmt.Errorf("headers are only valid for the http and sse transports")
		}
	default:
		return fmt.Errorf("transport must be one of: http, sse, stdio")
	}
	if server.ToolTimeout < 0 {
		return fmt.Errorf("tool_timeout must not be negative")
	}
	if server.ToolTimeout == 0 {
		server.ToolTimeout = DefaultMCPToolTimeout
	}
	return nil
}

// MCPServerEnabled reports the effective enabled state (default true).
func MCPServerEnabled(server MCPServerConfig) bool {
	return server.Enabled == nil || *server.Enabled
}
