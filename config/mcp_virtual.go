package config

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
)

const envMCPVirtualServers = "MCP_VIRTUAL_SERVERS"

// MCPVirtualServerConfig declares a virtual MCP server: a named subset of the
// upstream servers served at /mcp/{name}, so a client configured with a URL
// alone can reach a curated mix. It filters, never grants: each member keeps
// its own user-path scope and tool filters.
type MCPVirtualServerConfig struct {
	// Description is an optional human-readable note.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`

	// Servers lists the member server slugs. Required.
	Servers []string `yaml:"servers" json:"servers"`

	// ToolDiscovery overrides mcp.tool_discovery for sessions on this virtual
	// server: "off" or "search". Empty inherits the gateway default.
	ToolDiscovery string `yaml:"tool_discovery,omitempty" json:"tool_discovery,omitempty"`
}

// applyMCPVirtualEnv parses MCP_VIRTUAL_SERVERS — a JSON object mapping
// virtual server names to definitions — and merges it over the YAML map, the
// same way MCP_SERVERS merges over mcp.servers.
func applyMCPVirtualEnv(cfg *Config) error {
	raw := strings.TrimSpace(os.Getenv(envMCPVirtualServers))
	if raw == "" {
		return nil
	}
	var fromEnv map[string]MCPVirtualServerConfig
	if err := json.Unmarshal([]byte(raw), &fromEnv); err != nil {
		return fmt.Errorf("invalid %s: %w", envMCPVirtualServers, err)
	}
	if len(fromEnv) == 0 {
		return nil
	}
	if cfg.MCP.VirtualServers == nil {
		cfg.MCP.VirtualServers = make(map[string]MCPVirtualServerConfig, len(fromEnv))
	}
	// YAML keys are not canonicalized yet, so match them by canonical name:
	// an env "coding" must replace a YAML "Coding", not sit beside it.
	yamlKeys := make(map[string]string, len(cfg.MCP.VirtualServers))
	for name := range cfg.MCP.VirtualServers {
		yamlKeys[canonicalTextKey(name)] = name
	}
	seen := make(map[string]string, len(fromEnv))
	for name, virtual := range fromEnv {
		canonical := canonicalTextKey(name)
		if previous, dup := seen[canonical]; dup {
			return fmt.Errorf("%s: entries %q and %q both canonicalize to virtual server name %q", envMCPVirtualServers, previous, name, canonical)
		}
		seen[canonical] = name
		virtual.Description = expandString(virtual.Description)
		virtual.ToolDiscovery = expandString(virtual.ToolDiscovery)
		for i := range virtual.Servers {
			virtual.Servers[i] = expandString(virtual.Servers[i])
		}
		if yamlKey, ok := yamlKeys[canonical]; ok {
			delete(cfg.MCP.VirtualServers, yamlKey)
		}
		cfg.MCP.VirtualServers[canonical] = virtual
	}
	return nil
}

// normalizeMCPVirtualServers canonicalizes virtual server names and members
// and rejects definitions that cannot be served. Virtual server names share
// /mcp/{name} with server slugs, so a clash with a declared server fails
// startup. Members are not checked for existence here: admin-managed servers
// live in the store, so the gateway resolves members at runtime instead.
func normalizeMCPVirtualServers(cfg *MCPConfig) error {
	if len(cfg.VirtualServers) == 0 {
		return nil
	}
	names := make([]string, 0, len(cfg.VirtualServers))
	normalized := make(map[string]MCPVirtualServerConfig, len(cfg.VirtualServers))
	for name, virtual := range cfg.VirtualServers {
		canonical := canonicalTextKey(name)
		if err := validateMCPVirtualServerName(canonical); err != nil {
			return fmt.Errorf("mcp.virtual_servers[%q]: %w", name, err)
		}
		if _, dup := normalized[canonical]; dup {
			return fmt.Errorf("mcp.virtual_servers: duplicate virtual server name %q", canonical)
		}
		if _, clash := cfg.Servers[canonical]; clash {
			return fmt.Errorf("mcp.virtual_servers[%q]: name clashes with mcp.servers[%q]; virtual server names and server slugs share /mcp/{name}", canonical, canonical)
		}
		names = append(names, canonical)
		normalized[canonical] = virtual
	}
	sort.Strings(names)
	for _, name := range names {
		virtual := normalized[name]
		if err := normalizeMCPVirtualServer(&virtual, normalized); err != nil {
			return fmt.Errorf("mcp.virtual_servers[%q]: %w", name, err)
		}
		normalized[name] = virtual
	}
	cfg.VirtualServers = normalized
	return nil
}

// validateMCPVirtualServerName applies the server slug rules: the name is a
// /mcp/{name} URL segment and prefixes nothing, but sharing the rules keeps
// one alphabet for that path segment.
func validateMCPVirtualServerName(name string) error {
	if name == "" {
		return fmt.Errorf("virtual server name is required")
	}
	if len(name) > maxMCPServerSlugLength {
		return fmt.Errorf("virtual server name exceeds %d characters", maxMCPServerSlugLength)
	}
	if !mcpServerSlugRegex.MatchString(name) {
		return fmt.Errorf("virtual server name %q must match %s", name, mcpServerSlugRegex.String())
	}
	return nil
}

func normalizeMCPVirtualServer(virtual *MCPVirtualServerConfig, virtuals map[string]MCPVirtualServerConfig) error {
	virtual.Description = strings.TrimSpace(virtual.Description)
	switch mode := strings.ToLower(strings.TrimSpace(virtual.ToolDiscovery)); mode {
	case "", MCPToolDiscoveryOff, MCPToolDiscoverySearch:
		virtual.ToolDiscovery = mode
	default:
		return fmt.Errorf("tool_discovery must be %q or %q, got %q", MCPToolDiscoveryOff, MCPToolDiscoverySearch, virtual.ToolDiscovery)
	}
	members := make([]string, 0, len(virtual.Servers))
	for _, raw := range virtual.Servers {
		member := canonicalTextKey(raw)
		if member == "" {
			continue
		}
		if err := ValidateMCPServerSlug(member); err != nil {
			return fmt.Errorf("member %q: %w", raw, err)
		}
		if _, nested := virtuals[member]; nested {
			return fmt.Errorf("member %q is a virtual server; virtual servers can only include MCP servers", member)
		}
		if !slices.Contains(members, member) {
			members = append(members, member)
		}
	}
	if len(members) == 0 {
		return fmt.Errorf("servers is required: list at least one MCP server slug")
	}
	virtual.Servers = members
	return nil
}
