package providers

import (
	"log/slog"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
)

// BuiltinMCPServer is an MCP server a provider hosts for its customers,
// reachable with the same API key as the provider's models.
type BuiltinMCPServer struct {
	// Suffix completes the gateway slug: provider "zai" and suffix
	// "web-search" are served as "zai-web-search".
	Suffix string
	// URL is the provider's streamable HTTP MCP endpoint.
	URL string
	// Description is shown on the dashboard's MCP Servers page.
	Description string
}

// BuiltinMCPServersFunc lists a provider type's hosted MCP servers for one
// provider's resolved base URL, so they follow a regional or proxied host.
type BuiltinMCPServersFunc func(baseURL string) []BuiltinMCPServer

// builtinMCPServers returns the hosted MCP servers of providerType, if any.
func (f *ProviderFactory) builtinMCPServers(providerType, baseURL string) []BuiltinMCPServer {
	f.mu.RLock()
	list := f.mcpServers[providerType]
	f.mu.RUnlock()
	if list == nil {
		return nil
	}
	return list(baseURL)
}

// ProviderMCPServers declares the hosted MCP servers of the named providers
// (mcp.provider_servers) as MCP gateway servers. Each one authenticates with
// its provider's primary API key and is only sent to the host the provider's
// base URL already points at. A name that is not a configured provider, or
// whose type hosts no MCP server, is skipped with a warning.
func (r *InitResult) ProviderMCPServers(names []string) map[string]config.MCPServerConfig {
	if r == nil || r.Factory == nil || len(names) == 0 {
		return nil
	}
	return providerMCPServers(r.Factory, r.CredentialResolvedProviders, names)
}

func providerMCPServers(factory *ProviderFactory, resolved map[string]config.RawProviderConfig, names []string) map[string]config.MCPServerConfig {
	servers := make(map[string]config.MCPServerConfig)
	for _, name := range names {
		provider, ok := resolved[name]
		if !ok {
			slog.Warn("mcp.provider_servers: no configured provider with this name; skipping", "provider", core.LogText(name))
			continue
		}
		builtins := factory.builtinMCPServers(provider.Type, provider.BaseURL)
		if len(builtins) == 0 {
			slog.Warn("mcp.provider_servers: provider type hosts no MCP servers GoModel can expose; skipping",
				"provider", core.LogText(name), "type", provider.Type)
			continue
		}
		if provider.APIKey == "" {
			slog.Warn("mcp.provider_servers: provider has no API key; skipping", "provider", core.LogText(name))
			continue
		}
		prefix := config.DeriveMCPServerSlug(name)
		for _, builtin := range builtins {
			slug := prefix + "-" + builtin.Suffix
			server := config.MCPServerConfig{
				URL:         builtin.URL,
				Transport:   config.MCPTransportHTTP,
				Headers:     map[string]string{"Authorization": "Bearer " + provider.APIKey},
				Description: builtin.Description,
			}
			if err := config.ValidateMCPServerSlug(slug); err != nil {
				slog.Warn("mcp.provider_servers: skipping server", "provider", core.LogText(name), "error", err)
				continue
			}
			if err := config.ValidateMCPServerConfig(&server); err != nil {
				slog.Warn("mcp.provider_servers: skipping server", "provider", core.LogText(name), "server", slug, "error", err)
				continue
			}
			servers[slug] = server
		}
	}
	return servers
}
