package config

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/enterpilot/gomodel/internal/core"
	"golang.org/x/text/unicode/norm"
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

	// Servers maps stable server slugs to upstream definitions. Slugs become
	// tool namespaces and URL segments, so they are restricted to [a-z0-9_-].
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

// mcpServerSlugRegex stays inside the MCP tool-name alphabet because slugs
// prefix tools and prompts on the aggregated endpoint.
var mcpServerSlugRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

const (
	maxMCPServerSlugLength = 64
	maxMCPServerNameLength = 100
)

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
			return fmt.Errorf("%s: entries %q and %q both canonicalize to server slug %q", envMCPServers, previous, name, canonical)
		}
		seen[canonical] = name
		expandMCPServerEnv(&server)
		cfg.MCP.Servers[canonical] = server
	}
	return nil
}

// expandMCPServerEnv matches YAML configuration semantics after JSON decoding.
// Expanding typed values instead of the raw JSON keeps secrets containing
// quotes or backslashes from corrupting the MCP_SERVERS document.
func expandMCPServerEnv(server *MCPServerConfig) {
	server.URL = expandString(server.URL)
	server.Transport = expandString(server.Transport)
	server.Command = expandString(server.Command)
	server.Description = expandString(server.Description)
	for i := range server.Args {
		server.Args[i] = expandString(server.Args[i])
	}
	for i := range server.AllowedTools {
		server.AllowedTools[i] = expandString(server.AllowedTools[i])
	}
	for i := range server.DisallowedTools {
		server.DisallowedTools[i] = expandString(server.DisallowedTools[i])
	}
	for i := range server.UserPaths {
		server.UserPaths[i] = expandString(server.UserPaths[i])
	}
	for key, value := range server.Headers {
		server.Headers[key] = expandString(value)
	}
	for key, value := range server.Env {
		server.Env[key] = expandString(value)
	}
}

// normalizeMCPConfig canonicalizes server slugs, applies defaults, and rejects
// invalid entries. It runs at load time so a bad declaration fails startup
// loudly instead of silently dropping the server.
func normalizeMCPConfig(cfg *MCPConfig) error {
	if len(cfg.Servers) == 0 {
		return nil
	}
	normalized := make(map[string]MCPServerConfig, len(cfg.Servers))
	for name, server := range cfg.Servers {
		canonical := canonicalTextKey(name)
		if err := ValidateMCPServerSlug(canonical); err != nil {
			return fmt.Errorf("mcp.servers[%q]: %w", name, err)
		}
		if _, dup := normalized[canonical]; dup {
			return fmt.Errorf("mcp.servers: duplicate server slug %q", canonical)
		}
		if err := ValidateMCPServerConfig(&server); err != nil {
			return fmt.Errorf("mcp.servers[%q]: %w", canonical, err)
		}
		normalized[canonical] = server
	}
	cfg.Servers = normalized
	return nil
}

// ValidateMCPServerName accepts a human-facing Unicode display name. Machine
// constraints belong to the separate immutable slug.
func ValidateMCPServerName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("server name is required")
	}
	if utf8.RuneCountInString(name) > maxMCPServerNameLength {
		return fmt.Errorf("server name exceeds %d characters", maxMCPServerNameLength)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("server name must not contain control characters")
		}
	}
	return nil
}

// ValidateMCPServerSlug validates the stable ASCII identity used in routes,
// scope headers, and aggregated tool/prompt names.
func ValidateMCPServerSlug(slug string) error {
	if slug == "" {
		return fmt.Errorf("server slug is required")
	}
	if len(slug) > maxMCPServerSlugLength {
		return fmt.Errorf("server slug exceeds %d characters", maxMCPServerSlugLength)
	}
	if !mcpServerSlugRegex.MatchString(slug) {
		return fmt.Errorf("server slug %q must match %s", slug, mcpServerSlugRegex.String())
	}
	return nil
}

// DeriveMCPServerSlug creates a conservative default slug from a display
// name. Callers may let users edit it before creation; once persisted it is a
// stable identity and should not change when the display name changes.
func DeriveMCPServerSlug(name string) string {
	var b strings.Builder
	pendingSeparator := false
	normalized := norm.NFKD.String(strings.ToLower(strings.TrimSpace(name)))
	for _, r := range normalized {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if pendingSeparator && b.Len() > 0 && b.Len() < maxMCPServerSlugLength {
				b.WriteByte('-')
			}
			pendingSeparator = false
			if b.Len() >= maxMCPServerSlugLength {
				break
			}
			b.WriteRune(r)
			continue
		}
		pendingSeparator = b.Len() > 0
	}
	slug := strings.Trim(b.String(), "-_")
	if slug == "" {
		hash := uint32(2166136261)
		for _, r := range normalized {
			hash ^= uint32(r)
			hash *= 16777619
		}
		return fmt.Sprintf("mcp-%08x", hash)
	}
	return slug
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
	if len(server.UserPaths) > 0 {
		normalized := make([]string, 0, len(server.UserPaths))
		for _, raw := range server.UserPaths {
			path, err := core.NormalizeUserPath(raw)
			if err != nil {
				return fmt.Errorf("invalid user_paths value %q: %w", raw, err)
			}
			if path != "" && !slices.Contains(normalized, path) {
				normalized = append(normalized, path)
			}
		}
		slices.Sort(normalized)
		server.UserPaths = normalized
	}
	return nil
}

// MCPServerEnabled reports the effective enabled state (default true).
func MCPServerEnabled(server MCPServerConfig) bool {
	return server.Enabled == nil || *server.Enabled
}
