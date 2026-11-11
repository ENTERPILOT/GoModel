package mcpgateway

import (
	"fmt"
	"slices"
	"strings"
)

// ToolCatalogFilter narrows ToolCatalog to some servers. Empty fields do not
// filter; set fields combine.
type ToolCatalogFilter struct {
	// Server keeps one server, by slug.
	Server string
	// VirtualServer keeps the members of one served virtual server.
	VirtualServer string
	// UserPath keeps the servers a caller with this user path may see.
	UserPath string
}

// ToolCatalogServer is one server in the cross-server tool catalog, with the
// user-path scope that decides which callers see it.
type ToolCatalogServer struct {
	Server              string       `json:"server"`
	ServerName          string       `json:"server_name"`
	Transport           string       `json:"transport"`
	Enabled             bool         `json:"enabled"`
	Status              ServerStatus `json:"status"`
	UserPaths           []string     `json:"user_paths"`
	DisallowedUserPaths []string     `json:"disallowed_user_paths"`
}

// ToolCatalogTool is one discovered tool with its server. Name is the name
// clients call on the aggregated /mcp endpoint, empty when the server's tool
// filters hide the tool; UpstreamName is the server's own name for it, which
// /mcp/{server} serves. ReadOnly and Destructive are the upstream's explicit
// hints (see CatalogFeature).
type ToolCatalogTool struct {
	ToolCatalogServer
	Name         string `json:"name"`
	UpstreamName string `json:"upstream_name"`
	Description  string `json:"description,omitempty"`
	ReadOnly     bool   `json:"read_only"`
	Destructive  bool   `json:"destructive"`
	// Exposed is false for a tool the server's allowed_tools /
	// disallowed_tools hide from clients.
	Exposed bool `json:"exposed"`
}

// ToolCatalog is the tool inventory across servers: every server in scope,
// then every tool they list. A server with no catalog (disabled, or never
// reached) is in Servers with its status and has no tools.
type ToolCatalog struct {
	Servers []ToolCatalogServer `json:"servers"`
	Tools   []ToolCatalogTool   `json:"tools"`
}

// ToolCatalog returns every discovered tool of the servers the filter keeps,
// sorted by server slug and tool name, from the catalogs already in memory:
// it never dials an upstream, so an unreachable server cannot fail it. An
// unknown server, or a virtual server that is unknown or not served, wraps
// ErrNotFound.
func (s *Service) ToolCatalog(filter ToolCatalogFilter) (ToolCatalog, error) {
	if filter.Server != "" {
		if _, ok := s.manager.get(filter.Server); !ok {
			return ToolCatalog{}, fmt.Errorf("%w: %s", ErrNotFound, filter.Server)
		}
	}
	var members []string
	if filter.VirtualServer != "" {
		if !s.servedVirtual(filter.VirtualServer) {
			return ToolCatalog{}, fmt.Errorf("%w: virtual server %q is unknown or not served", ErrNotFound, filter.VirtualServer)
		}
		spec, _ := s.virtualSpec(filter.VirtualServer)
		members = spec.Servers
	}

	result := ToolCatalog{Servers: []ToolCatalogServer{}, Tools: []ToolCatalogTool{}}
	for _, view := range s.manager.Views() {
		spec := view.Spec
		if filter.Server != "" && spec.Name != filter.Server {
			continue
		}
		if filter.VirtualServer != "" && !slices.Contains(members, spec.Name) {
			continue
		}
		if filter.UserPath != "" && !spec.visibleTo(filter.UserPath) {
			continue
		}
		server := ToolCatalogServer{
			Server:              spec.Name,
			ServerName:          spec.DisplayName,
			Transport:           spec.Transport,
			Enabled:             spec.Enabled,
			Status:              view.Status,
			UserPaths:           append([]string{}, spec.UserPaths...),
			DisallowedUserPaths: append([]string{}, spec.DisallowedUserPaths...),
		}
		if server.ServerName == "" {
			server.ServerName = spec.Name
		}
		result.Servers = append(result.Servers, server)

		catalog, _ := s.Catalog(spec.Name)
		first := len(result.Tools)
		for _, tool := range catalog.Tools {
			result.Tools = append(result.Tools, toolCatalogTool(server, tool, true))
		}
		for _, tool := range catalog.ExcludedTools {
			result.Tools = append(result.Tools, toolCatalogTool(server, tool, false))
		}
		slices.SortFunc(result.Tools[first:], func(a, b ToolCatalogTool) int {
			return strings.Compare(a.UpstreamName, b.UpstreamName)
		})
	}
	return result, nil
}

func toolCatalogTool(server ToolCatalogServer, tool CatalogFeature, exposed bool) ToolCatalogTool {
	row := ToolCatalogTool{
		ToolCatalogServer: server,
		UpstreamName:      tool.Name,
		Description:       tool.Description,
		ReadOnly:          tool.ReadOnly,
		Destructive:       tool.Destructive,
		Exposed:           exposed,
	}
	if exposed {
		row.Name = NamespacedName(server.Server, tool.Name)
	}
	return row
}
