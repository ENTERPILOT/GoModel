package mcpgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/enterpilot/gomodel/config"
)

// ToolDiscoveryHeader overrides mcp.tool_discovery for one session ("off" or
// "search"), so a client without its own tool search can opt in while other
// clients of the same gateway keep the full tool list. Read at initialize;
// unknown values fall back to the configured default.
const ToolDiscoveryHeader = "X-MCP-Tool-Discovery"

// Meta-tool names served instead of the catalog in search discovery mode.
// CallToolName is exported so the request log can label a relayed call with
// the tool it runs.
const (
	searchToolsName = "search_tools"
	CallToolName    = "call_tool"
)

const (
	defaultSearchLimit = 5
	maxSearchLimit     = 20
)

// discoveryMode resolves the session's discovery mode from the header, falling
// back to the configured default.
func (s *Service) discoveryMode(r *http.Request) bool {
	switch strings.ToLower(strings.TrimSpace(r.Header.Get(ToolDiscoveryHeader))) {
	case config.MCPToolDiscoverySearch:
		return true
	case config.MCPToolDiscoveryOff:
		return false
	}
	return s.searchDiscovery
}

// indexedTool is one searchable tool with its pre-tokenized fields.
type indexedTool struct {
	exposed  string
	upstream string
	tool     *mcp.Tool

	nameTerms   []string
	detailTerms []string // title and parameter names
	descTerms   []string
}

// toolIndex is a discovery session's tool snapshot, mirroring what tools/list
// would have returned. Like registered tools, it is fixed at initialize;
// visibility and tool filters are re-checked on every search and call.
type toolIndex struct {
	tools   []indexedTool
	aliases map[string]string // unambiguous bare name -> exposed name
}

func (idx *toolIndex) add(exposed, upstream string, tool *mcp.Tool) {
	idx.tools = append(idx.tools, indexedTool{
		exposed:     exposed,
		upstream:    upstream,
		tool:        tool,
		nameTerms:   searchTerms(exposed),
		detailTerms: searchTerms(tool.Title + " " + strings.Join(schemaPropertyNames(tool.InputSchema), " ")),
		descTerms:   searchTerms(tool.Description),
	})
}

func (idx *toolIndex) lookup(name string) (indexedTool, bool) {
	if exposed, ok := idx.aliases[name]; ok {
		name = exposed
	}
	for _, tool := range idx.tools {
		if tool.exposed == name {
			return tool, true
		}
	}
	return indexedTool{}, false
}

// rankTools ranks tools by weighted keyword matches: a term found in the tool
// name counts most, then title and parameter names, then the description.
// Each term is weighted by how rare it is, so a server prefix shared by every
// tool does not drown out the words that tell tools apart.
func rankTools(query string, candidates []indexedTool, limit int) []indexedTool {
	// No early return on an empty term list: a stop word or single letter
	// yields no terms but can still be an exact tool name.
	terms := dedupe(searchTerms(query))
	if len(candidates) == 0 {
		return nil
	}
	exact := strings.ToLower(strings.TrimSpace(query))

	type scored struct {
		tool  indexedTool
		score float64
	}
	weights := make([][]float64, len(candidates))
	docFreq := make([]int, len(terms))
	for i, tool := range candidates {
		weights[i] = make([]float64, len(terms))
		for j, term := range terms {
			switch {
			case termIn(term, tool.nameTerms):
				weights[i][j] = 3
			case termIn(term, tool.detailTerms):
				weights[i][j] = 2
			case termIn(term, tool.descTerms):
				weights[i][j] = 1
			}
			if weights[i][j] > 0 {
				docFreq[j]++
			}
		}
	}

	results := make([]scored, 0, len(candidates))
	for i, tool := range candidates {
		score := 0.0
		for j := range terms {
			if weights[i][j] > 0 {
				score += weights[i][j] * math.Log(1+float64(len(candidates))/float64(docFreq[j]))
			}
		}
		if strings.EqualFold(tool.exposed, exact) || strings.EqualFold(tool.tool.Name, exact) {
			score += 100
		}
		if score > 0 {
			results = append(results, scored{tool: tool, score: score})
		}
	}
	sort.SliceStable(results, func(a, b int) bool { return results[a].score > results[b].score })
	if len(results) > limit {
		results = results[:limit]
	}
	out := make([]indexedTool, len(results))
	for i, result := range results {
		out[i] = result.tool
	}
	return out
}

// termIn reports whether a query term matches a tool term. A query term of
// three or more letters matches as a prefix ("repo" finds "repository"), and
// a tool term matches a query term that only adds a short suffix ("issues"
// finds "issue"), covering plurals and simple stems without a stemmer.
func termIn(term string, toolTerms []string) bool {
	for _, candidate := range toolTerms {
		if candidate == term ||
			(len(term) >= 3 && strings.HasPrefix(candidate, term)) ||
			(len(candidate) >= 3 && len(term)-len(candidate) <= 2 && strings.HasPrefix(term, candidate)) {
			return true
		}
	}
	return false
}

// stopWords carry no signal for telling tools apart.
var stopWords = map[string]struct{}{
	"an": {}, "and": {}, "are": {}, "by": {}, "for": {}, "from": {}, "in": {},
	"into": {}, "is": {}, "it": {}, "of": {}, "on": {}, "or": {}, "the": {},
	"this": {}, "to": {}, "with": {},
}

// searchTerms lowercases text and splits it into words on punctuation,
// underscores, and camelCase boundaries. Single characters and stop words
// are dropped.
func searchTerms(text string) []string {
	var terms []string
	var current []rune
	flush := func() {
		if len(current) > 1 {
			term := strings.ToLower(string(current))
			if _, stop := stopWords[term]; !stop {
				terms = append(terms, term)
			}
		}
		current = current[:0]
	}
	var prev rune
	for _, r := range text {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
		case unicode.IsUpper(r) && unicode.IsLower(prev):
			flush()
			current = append(current, r)
		default:
			current = append(current, r)
		}
		prev = r
	}
	flush()
	return terms
}

func dedupe(terms []string) []string {
	seen := make(map[string]struct{}, len(terms))
	out := terms[:0]
	for _, term := range terms {
		if _, ok := seen[term]; ok {
			continue
		}
		seen[term] = struct{}{}
		out = append(out, term)
	}
	return out
}

// schemaPropertyNames returns the top-level property names of a tool's input
// schema, whatever Go type the SDK decoded it into.
func schemaPropertyNames(schema any) []string {
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil
	}
	var parsed struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil
	}
	names := make([]string, 0, len(parsed.Properties))
	for name := range parsed.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// registerDiscoveryTools serves search_tools and call_tool in place of the
// session's tools. The list the client sees never changes, which keeps
// provider prompt caches warm; each call is logged under the real tool name.
func (s *Service) registerDiscoveryTools(server *mcp.Server, idx *toolIndex, endpoint string) {
	server.AddTool(&mcp.Tool{
		Name:        searchToolsName,
		Description: searchToolsDescription(idx),
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Keywords describing the task, such as \"create github issue\", or an exact tool name.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     maxSearchLimit,
					"description": fmt.Sprintf("Maximum number of tools to return. Default %d.", defaultSearchLimit),
				},
			},
			"required": []string{"query"},
		},
		Annotations: &mcp.ToolAnnotations{Title: "Search tools", ReadOnlyHint: true},
	}, s.searchToolsHandler(idx))

	server.AddTool(&mcp.Tool{
		Name:        CallToolName,
		Description: "Call a tool found with " + searchToolsName + ". Pass its exact name and arguments matching its input schema.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Tool name as returned by " + searchToolsName + ".",
				},
				"arguments": map[string]any{
					"type":        "object",
					"description": "Arguments for the tool, matching its input schema.",
				},
			},
			"required": []string{"name"},
		},
	}, s.callToolHandler(idx, endpoint))
}

func searchToolsDescription(idx *toolIndex) string {
	counts := make(map[string]int)
	var servers []string
	for _, tool := range idx.tools {
		if counts[tool.upstream] == 0 {
			servers = append(servers, tool.upstream)
		}
		counts[tool.upstream]++
	}
	parts := make([]string, len(servers))
	for i, server := range servers {
		parts[i] = fmt.Sprintf("%s (%d)", server, counts[server])
	}
	desc := "Search the tools available through this gateway by keyword. Returns matching tool names, descriptions, and input schemas; run one with " + CallToolName + "."
	if len(parts) > 0 {
		desc += " Servers: " + strings.Join(parts, ", ") + "."
	}
	return desc
}

// searchResult is one tool in a search_tools response.
type searchResult struct {
	Name        string               `json:"name"`
	Title       string               `json:"title,omitempty"`
	Description string               `json:"description,omitempty"`
	InputSchema any                  `json:"inputSchema"`
	Annotations *mcp.ToolAnnotations `json:"annotations,omitempty"`
}

func (s *Service) searchToolsHandler(idx *toolIndex) mcp.ToolHandler {
	return func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		if err := unmarshalArguments(req, &args); err != nil {
			return toolError("invalid " + searchToolsName + " arguments: " + err.Error()), nil
		}
		if strings.TrimSpace(args.Query) == "" {
			return toolError("query is required"), nil
		}
		limit := args.Limit
		if limit <= 0 {
			limit = defaultSearchLimit
		}
		limit = min(limit, maxSearchLimit)

		matches := rankTools(args.Query, s.callableTools(req.Session, idx), limit)
		if len(matches) == 0 {
			return textResult(fmt.Sprintf("No tools matched %q. Try fewer or broader keywords.", args.Query)), nil
		}
		results := make([]searchResult, len(matches))
		for i, match := range matches {
			results[i] = searchResult{
				Name:        match.exposed,
				Title:       match.tool.Title,
				Description: match.tool.Description,
				InputSchema: match.tool.InputSchema,
				Annotations: match.tool.Annotations,
			}
		}
		encoded, err := json.Marshal(map[string]any{"tools": results})
		if err != nil {
			return nil, fmt.Errorf("encode search results: %w", err)
		}
		return textResult(string(encoded)), nil
	}
}

func (s *Service) callToolHandler(idx *toolIndex, endpoint string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := unmarshalArguments(req, &args); err != nil {
			return toolError("invalid " + CallToolName + " arguments: " + err.Error()), nil
		}
		target, ok := idx.lookup(strings.TrimSpace(args.Name))
		if !ok {
			return toolError(fmt.Sprintf("unknown tool %q; use %s to find tool names", args.Name, searchToolsName)), nil
		}
		// Dispatch through the regular handler so session authorization,
		// tool filters, and usage accounting match a direct tools/call.
		inner := &mcp.CallToolRequest{
			Session: req.Session,
			Extra:   req.Extra,
			Params:  &mcp.CallToolParamsRaw{Name: target.exposed, Arguments: toolArguments(args.Arguments)},
		}
		result, err := s.toolHandler(target.upstream, target.tool.Name, target.exposed, endpoint)(ctx, inner)
		if err != nil {
			// Clients relay a tool error to the model but may only surface a
			// JSON-RPC error to the user, so the model could not recover.
			return toolError(err.Error()), nil
		}
		return result, nil
	}
}

// callableTools drops index entries the session may no longer use: servers
// hidden from its user path since initialize, and tools excluded by an edited
// filter. Search must not advertise what call_tool would reject.
func (s *Service) callableTools(session *mcp.ServerSession, idx *toolIndex) []indexedTool {
	allowed := make(map[string]bool)
	out := make([]indexedTool, 0, len(idx.tools))
	for _, tool := range idx.tools {
		visible, checked := allowed[tool.upstream]
		if !checked {
			visible = s.authorizeSession(session, tool.upstream) == nil
			allowed[tool.upstream] = visible
		}
		if !visible {
			continue
		}
		if u, ok := s.manager.get(tool.upstream); !ok || !u.toolExposed(tool.tool.Name) {
			continue
		}
		out = append(out, tool)
	}
	return out
}

// toolArguments normalizes call_tool's arguments: null means none, and a JSON
// object sent as a string — a common model mistake — is unwrapped.
func toolArguments(raw json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if trimmed[0] == '"' {
		var encoded string
		if err := json.Unmarshal(trimmed, &encoded); err == nil && json.Valid([]byte(encoded)) {
			return json.RawMessage(encoded)
		}
	}
	return trimmed
}

func unmarshalArguments(req *mcp.CallToolRequest, dst any) error {
	if req.Params == nil || len(req.Params.Arguments) == 0 {
		return nil
	}
	return json.Unmarshal(req.Params.Arguments, dst)
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func toolError(text string) *mcp.CallToolResult {
	result := textResult(text)
	result.IsError = true
	return result
}
