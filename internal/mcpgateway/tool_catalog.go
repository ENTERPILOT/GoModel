package mcpgateway

import (
	"fmt"
	"slices"
	"strings"
)

// ToolCatalogFilter narrows ToolCatalog. Empty fields do not filter; set
// fields combine.
type ToolCatalogFilter struct {
	// Server keeps the rows of one server, by slug. It does not change which
	// server owns a /mcp name: that follows the endpoint the other two
	// fields describe.
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

// ToolCatalogTool is one discovered tool with its server. UpstreamName is the
// server's own name for it, which /mcp/{server} serves. ReadOnly and
// Destructive are the upstream's explicit hints (see CatalogFeature).
type ToolCatalogTool struct {
	ToolCatalogServer
	// Name is the name that reaches this tool on the aggregated endpoint
	// (/mcp, or the virtual server in the filter). It is empty when no such
	// name exists: the tool is not exposed, or it is shadowed.
	Name         string `json:"name"`
	UpstreamName string `json:"upstream_name"`
	Description  string `json:"description,omitempty"`
	ReadOnly     bool   `json:"read_only"`
	Destructive  bool   `json:"destructive"`
	// Exposed is false for a tool the server's allowed_tools /
	// disallowed_tools hide from clients. It says nothing about shadowing.
	Exposed bool `json:"exposed"`
	// ShadowedBy names the server that owns this tool's aggregated name:
	// two servers produced the same name and the gateway keeps the first
	// in slug order, so that name reaches the other server's tool. The tool
	// stays callable on /mcp/{server} under UpstreamName.
	ShadowedBy string `json:"shadowed_by,omitempty"`
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
// it never dials an upstream, so an unreachable server cannot fail it. Names
// are owned as on the endpoint the filter describes: among the virtual
// server's members and the servers the user path sees, or among all servers
// without those. An unknown server, or a virtual server that is unknown or
// not served, wraps ErrNotFound.
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
	// Views are in slug order, the order sessions register servers in, so
	// the first claim of a name here is the server the gateway keeps.
	owners := make(map[string]string)
	for _, view := range s.manager.Views() {
		spec := view.Spec
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
		// Every server on the endpoint claims its names, also one the
		// server filter leaves out of the rows.
		tools := s.serverToolRows(server, owners)
		if filter.Server != "" && spec.Name != filter.Server {
			continue
		}
		result.Servers = append(result.Servers, server)
		result.Tools = append(result.Tools, tools...)
	}
	return result, nil
}

// serverToolRows lists one server's tools by upstream name, one row per name:
// the gateway registers the first of a repeated name. Each exposed tool
// claims its aggregated name in owners; a tool whose name an earlier server
// holds gets no name and that server in ShadowedBy.
func (s *Service) serverToolRows(server ToolCatalogServer, owners map[string]string) []ToolCatalogTool {
	catalog, _ := s.Catalog(server.Server)
	rows := make([]ToolCatalogTool, 0, len(catalog.Tools)+len(catalog.ExcludedTools))
	for _, tool := range catalog.Tools {
		rows = append(rows, toolCatalogRow(server, tool, true))
	}
	for _, tool := range catalog.ExcludedTools {
		rows = append(rows, toolCatalogRow(server, tool, false))
	}
	slices.SortStableFunc(rows, func(a, b ToolCatalogTool) int {
		return strings.Compare(a.UpstreamName, b.UpstreamName)
	})
	rows = slices.CompactFunc(rows, func(a, b ToolCatalogTool) bool {
		return a.UpstreamName == b.UpstreamName
	})
	for i := range rows {
		if !rows[i].Exposed {
			continue
		}
		name := NamespacedName(server.Server, rows[i].UpstreamName)
		if owner, claimed := claimToolName(owners, name, server.Server); claimed {
			rows[i].Name = name
		} else {
			rows[i].ShadowedBy = owner
		}
	}
	return rows
}

func toolCatalogRow(server ToolCatalogServer, tool CatalogFeature, exposed bool) ToolCatalogTool {
	return ToolCatalogTool{
		ToolCatalogServer: server,
		UpstreamName:      tool.Name,
		Description:       tool.Description,
		ReadOnly:          tool.ReadOnly,
		Destructive:       tool.Destructive,
		Exposed:           exposed,
	}
}
