package mcpgateway

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"

	"github.com/enterpilot/gomodel/config"
)

// VirtualServerSpec is one virtual MCP server: a named subset of the upstream
// servers served at /mcp/{name}. It filters, never grants — every member
// keeps its own user-path scope and tool filters.
type VirtualServerSpec struct {
	Name        string
	Description string
	// Servers are the member server slugs, in declaration order.
	Servers []string
	// ToolDiscovery is the session default for this endpoint: "off",
	// "search", or "" to inherit the gateway default.
	ToolDiscovery string
}

// VirtualFromConfig converts one declarative entry into a runtime spec.
func VirtualFromConfig(name string, cfg config.MCPVirtualServerConfig) VirtualServerSpec {
	return VirtualServerSpec{
		Name:          name,
		Description:   cfg.Description,
		Servers:       slices.Clone(cfg.Servers),
		ToolDiscovery: cfg.ToolDiscovery,
	}
}

// VirtualServerView is the admin snapshot of one virtual server.
type VirtualServerView struct {
	Spec VirtualServerSpec
	// ToolDiscovery is the effective default mode, "off" or "search".
	ToolDiscovery string
	// MissingServers lists members no configured server matches.
	MissingServers []string
	// Conflict explains why the virtual server is not served; empty when it is.
	Conflict string
}

// IsVirtual reports whether name is a declared virtual server, which reserves
// /mcp/{name} for it.
func (s *Service) IsVirtual(name string) bool {
	_, ok := s.virtualSpecs[name]
	return ok
}

// VirtualViews returns every virtual server sorted by name.
func (s *Service) VirtualViews() []VirtualServerView {
	views := make([]VirtualServerView, 0, len(s.virtualSpecs))
	for _, spec := range s.virtualSpecs {
		view := VirtualServerView{
			Spec:          spec,
			ToolDiscovery: config.MCPToolDiscoveryOff,
			Conflict:      s.virtualConflict(spec.Name),
		}
		if s.virtualDiscovery(spec.Name) {
			view.ToolDiscovery = config.MCPToolDiscoverySearch
		}
		for _, member := range spec.Servers {
			if _, ok := s.manager.get(member); !ok {
				view.MissingServers = append(view.MissingServers, member)
			}
		}
		views = append(views, view)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Spec.Name < views[j].Spec.Name })
	return views
}

// servedVirtual reports whether /mcp/{name} serves a virtual server. A server
// with the same slug wins: a virtual server never hides a real one.
func (s *Service) servedVirtual(name string) bool {
	return s.IsVirtual(name) && s.virtualConflict(name) == ""
}

// virtualConflict explains why a declared virtual server is not served, or
// returns "" when it is. Config validation already rejects clashes with
// declared servers, so only an admin-managed server can claim the name.
func (s *Service) virtualConflict(name string) string {
	if _, taken := s.manager.get(name); !taken {
		return ""
	}
	return fmt.Sprintf("virtual MCP server %q is not served: MCP server %q uses the same name; rename one of them", name, name)
}

// virtualDiscovery is the default discovery mode for sessions on a virtual
// server that do not send ToolDiscoveryHeader.
func (s *Service) virtualDiscovery(name string) bool {
	switch s.virtualSpecs[name].ToolDiscovery {
	case config.MCPToolDiscoverySearch:
		return true
	case config.MCPToolDiscoveryOff:
		return false
	}
	return s.searchDiscovery
}

// ErrVirtualNameTaken marks a new admin-managed server whose slug a virtual
// server already serves at /mcp/{name}.
var ErrVirtualNameTaken = errors.New("slug is used by a virtual MCP server")

// VirtualNameTakenError explains ErrVirtualNameTaken for one slug; it
// matches ErrVirtualNameTaken under errors.Is.
func VirtualNameTakenError(name string) error {
	return virtualNameTakenError{name: name}
}

type virtualNameTakenError struct{ name string }

func (e virtualNameTakenError) Error() string {
	return fmt.Sprintf("slug %q is used by virtual MCP server %q (declared in config); choose another slug", e.name, e.name)
}

func (e virtualNameTakenError) Is(target error) bool { return target == ErrVirtualNameTaken }

// logVirtualServerIssues reports virtual servers that are not served and
// members that match no server, so a typo in config surfaces at startup and
// after every reload.
func (s *Service) logVirtualServerIssues() {
	for _, view := range s.VirtualViews() {
		if view.Conflict != "" {
			slog.Error("virtual mcp server is not served", "virtual_server", view.Spec.Name, "reason", view.Conflict)
		}
		var missing, nested []string
		for _, member := range view.MissingServers {
			if s.IsVirtual(member) {
				nested = append(nested, member)
			} else {
				missing = append(missing, member)
			}
		}
		if len(missing) > 0 {
			slog.Warn("virtual mcp server members match no mcp server; they are skipped",
				"virtual_server", view.Spec.Name, "missing", missing)
		}
		if len(nested) > 0 {
			slog.Warn("virtual mcp server members are virtual servers; virtual servers can only include MCP servers, so they are skipped",
				"virtual_server", view.Spec.Name, "members", nested)
		}
	}
}
