package mcpgateway

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/enterpilot/gomodel/internal/core"
)

// namespaceSeparator joins server name and tool/prompt name on the aggregated
// endpoint. Underscore stays inside the MCP tool-name alphabet and inside the
// stricter charsets providers enforce on function names.
const namespaceSeparator = "_"

// catalog is an immutable snapshot of one upstream's listed features. Tools
// and prompts keep their original (un-prefixed) names and raw schemas; the
// per-session server view applies namespacing.
type catalog struct {
	// discovered is every tool the upstream lists; tools is the subset the
	// operator tool filters expose. Keeping both lets filter edits apply
	// without re-listing and lets the admin inspector show hidden tools.
	discovered   []*mcp.Tool
	tools        []*mcp.Tool
	prompts      []*mcp.Prompt
	resources    []*mcp.Resource
	templates    []*mcp.ResourceTemplate
	instructions string
}

func (c *catalog) toolCount() int {
	if c == nil {
		return 0
	}
	return len(c.tools)
}

func (c *catalog) excludedToolCount() int {
	if c == nil {
		return 0
	}
	return len(c.discovered) - len(c.tools)
}

// withToolFilters returns a copy whose exposed tools reflect the given
// filters. Catalogs are immutable, so the copy shares every other list.
func (c *catalog) withToolFilters(allowed, disallowed []string) *catalog {
	if c == nil {
		return nil
	}
	next := *c
	next.tools = filterTools(c.discovered, allowed, disallowed)
	return &next
}

func (c *catalog) promptCount() int {
	if c == nil {
		return 0
	}
	return len(c.prompts)
}

func (c *catalog) resourceCount() int {
	if c == nil {
		return 0
	}
	return len(c.resources) + len(c.templates)
}

// NamespacedName is the aggregated-endpoint name for one upstream feature.
func NamespacedName(server, name string) string {
	return server + namespaceSeparator + name
}

// discoverTools drops unnamed tools, normalizes schemas, and returns tools in
// deterministic (sorted) order. Deterministic ordering keeps downstream
// tools/list stable, which keeps provider prompt caches warm for clients that
// embed the tool list in prompts.
func discoverTools(tools []*mcp.Tool) []*mcp.Tool {
	discovered := make([]*mcp.Tool, 0, len(tools))
	for _, tool := range tools {
		if tool == nil || tool.Name == "" {
			continue
		}
		discovered = append(discovered, normalizeToolSchemas(tool))
	}
	slices.SortFunc(discovered, func(a, b *mcp.Tool) int {
		return strings.Compare(a.Name, b.Name)
	})
	return discovered
}

// filterTools applies the operator-level allow/deny lists to original tool
// names, preserving input order.
func filterTools(tools []*mcp.Tool, allowed, disallowed []string) []*mcp.Tool {
	filtered := make([]*mcp.Tool, 0, len(tools))
	for _, tool := range tools {
		if toolAllowed(tool.Name, allowed, disallowed) {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

// normalizeToolSchemas accepts imperfect upstream catalogs without letting
// them crash the downstream SDK, which requires object-shaped schemas. A
// missing or invalid input schema becomes a permissive object; an invalid
// optional output schema is omitted rather than making a false promise.
func normalizeToolSchemas(tool *mcp.Tool) *mcp.Tool {
	clone := *tool
	if !isObjectSchema(clone.InputSchema) {
		clone.InputSchema = map[string]any{"type": "object", "additionalProperties": true}
	}
	if clone.OutputSchema != nil && !isObjectSchema(clone.OutputSchema) {
		clone.OutputSchema = nil
	}
	return &clone
}

func isObjectSchema(schema any) bool {
	if schema == nil {
		return false
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return false
	}
	var value map[string]any
	if err := json.Unmarshal(encoded, &value); err != nil {
		return false
	}
	return value["type"] == "object"
}

func toolAllowed(name string, allowed, disallowed []string) bool {
	if len(allowed) > 0 && !slices.Contains(allowed, name) {
		return false
	}
	return !slices.Contains(disallowed, name)
}

// normalizeUserPaths canonicalizes and sorts a user-path scope list so
// userPathAllowed can binary-search it. Invalid entries are dropped.
func normalizeUserPaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(paths))
	for _, path := range paths {
		canonical, err := core.NormalizeUserPath(path)
		if err != nil || canonical == "" {
			continue
		}
		if !slices.Contains(normalized, canonical) {
			normalized = append(normalized, canonical)
		}
	}
	slices.Sort(normalized)
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

// userPathAllowed reports whether userPath falls inside one of the allowed
// subtrees. An empty allow list means everyone. Mirrors virtual models.
func userPathAllowed(userPath string, allowed []string) bool {
	return len(allowed) == 0 || userPathWithin(userPath, allowed)
}

// userPathWithin reports whether userPath falls inside one of the sorted,
// normalized subtrees. "/" matches every caller, including one without a
// user path; an empty list matches nobody.
func userPathWithin(userPath string, subtrees []string) bool {
	if len(subtrees) == 0 {
		return false
	}
	if _, ok := slices.BinarySearch(subtrees, "/"); ok {
		return true
	}
	userPath, err := core.NormalizeUserPath(userPath)
	if err != nil || userPath == "" {
		return false
	}
	for _, candidate := range core.UserPathAncestors(userPath) {
		if _, ok := slices.BinarySearch(subtrees, candidate); ok {
			return true
		}
	}
	return false
}
