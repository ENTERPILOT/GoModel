package mcpgateway

import (
	"fmt"
	"maps"
	"strings"

	"github.com/enterpilot/gomodel/config"
)

// accessWildcard is the tool part of a selector that grants a whole server.
const accessWildcard = "*"

// keyAccess is the MCP allowlist bound to a managed API key, parsed from its
// selectors: "server/*" grants a whole server (tools, prompts, and
// resources), "server/tool" grants one tool by its original upstream name.
// A nil *keyAccess means the key imposes no MCP restriction.
type keyAccess struct {
	servers map[string]struct{}
	tools   map[string]map[string]struct{}
}

// newKeyAccess parses canonical selectors. An empty list returns nil, which
// is unrestricted. Invalid entries grant nothing, so a list holding only
// invalid entries fails closed.
func newKeyAccess(selectors []string) *keyAccess {
	if len(selectors) == 0 {
		return nil
	}
	access := &keyAccess{
		servers: make(map[string]struct{}),
		tools:   make(map[string]map[string]struct{}),
	}
	for _, selector := range selectors {
		server, tool, err := parseAccessSelector(selector)
		if err != nil {
			continue
		}
		if tool == accessWildcard {
			access.servers[server] = struct{}{}
			continue
		}
		if access.tools[server] == nil {
			access.tools[server] = make(map[string]struct{})
		}
		access.tools[server][tool] = struct{}{}
	}
	return access
}

// equal reports whether two allowlists grant exactly the same servers and
// tools; nil (unrestricted) equals only nil.
func (a *keyAccess) equal(other *keyAccess) bool {
	if a == nil || other == nil {
		return a == other
	}
	return maps.Equal(a.servers, other.servers) &&
		maps.EqualFunc(a.tools, other.tools, func(x, y map[string]struct{}) bool { return maps.Equal(x, y) })
}

// server reports whether any selector reaches the server, so it is listed.
func (a *keyAccess) server(name string) bool {
	if a == nil {
		return true
	}
	if _, ok := a.servers[name]; ok {
		return true
	}
	return len(a.tools[name]) > 0
}

// wholeServer reports whether the whole server is granted. Prompts and
// resources need it: a single-tool grant exposes that tool only.
func (a *keyAccess) wholeServer(name string) bool {
	if a == nil {
		return true
	}
	_, ok := a.servers[name]
	return ok
}

// tool reports whether one tool, by its original upstream name, is granted.
func (a *keyAccess) tool(server, tool string) bool {
	if a.wholeServer(server) {
		return true
	}
	_, ok := a.tools[server][tool]
	return ok
}

// authorize checks one call against the allowlist: a tool call by its
// original tool name, or a prompt or resource read (toolName "") which needs
// the whole server granted.
func (a *keyAccess) authorize(server, toolName string) error {
	if toolName == "" {
		if !a.wholeServer(server) {
			return fmt.Errorf("mcp server %q: %w", server, ErrNotAllowedForKey)
		}
		return nil
	}
	if !a.tool(server, toolName) {
		return fmt.Errorf("mcp server %q tool %q: %w", server, toolName, ErrNotAllowedForKey)
	}
	return nil
}

// parseAccessSelector splits one selector into its server slug and tool
// name. A bare server slug means the whole server. Tool names are the
// original upstream names, the same ones allowed_tools uses.
func parseAccessSelector(selector string) (server, tool string, err error) {
	selector = strings.TrimSpace(selector)
	server, tool, found := strings.Cut(selector, "/")
	server = strings.TrimSpace(server)
	tool = strings.TrimSpace(tool)
	if !found {
		tool = accessWildcard
	}
	if err := config.ValidateMCPServerSlug(server); err != nil {
		return "", "", fmt.Errorf("invalid MCP selector %q: %w", selector, err)
	}
	if tool == "" {
		return "", "", fmt.Errorf("invalid MCP selector %q: tool name is required after %q", selector, server+"/")
	}
	return server, tool, nil
}

// NormalizeAccessSelectors validates MCP allowlist selectors and returns them
// in canonical form ("server/*" or "server/tool"), de-duplicated in order.
// A bare server slug is accepted for "server/*". An empty result means the
// key is not restricted.
func NormalizeAccessSelectors(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, selector := range raw {
		if strings.TrimSpace(selector) == "" {
			continue
		}
		server, tool, err := parseAccessSelector(selector)
		if err != nil {
			return nil, err
		}
		canonical := server + "/" + tool
		if _, dup := seen[canonical]; dup {
			continue
		}
		seen[canonical] = struct{}{}
		out = append(out, canonical)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// AccessCovers reports whether a key restricted to granted may also grant
// selector, so a restricted admin key cannot issue a broader one. An empty
// granted list covers everything; an empty selector means unrestricted and is
// covered only by an empty granted list.
func AccessCovers(granted []string, selector string) bool {
	access := newKeyAccess(granted)
	if access == nil {
		return true
	}
	if strings.TrimSpace(selector) == "" {
		return false
	}
	server, tool, err := parseAccessSelector(selector)
	if err != nil {
		return false
	}
	if tool == accessWildcard {
		return access.wholeServer(server)
	}
	return access.tool(server, tool)
}
