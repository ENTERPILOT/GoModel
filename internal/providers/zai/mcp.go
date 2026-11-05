package zai

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/enterpilot/gomodel/internal/providers"
)

// mcpServers lists the remote MCP servers Z.ai hosts for GLM Coding Plan keys
// (https://docs.z.ai/devpack/mcp). They live under the same "/api" root as the
// chat API, so a base URL on open.bigmodel.cn or behind a path-prefixed proxy
// reaches the matching MCP endpoints.
func mcpServers(baseURL string) []providers.BuiltinMCPServer {
	root, ok := apiRoot(providers.ResolveBaseURL(baseURL, defaultBaseURL))
	if !ok {
		return nil
	}
	return []providers.BuiltinMCPServer{
		{Suffix: "web-search", URL: root + "/mcp/web_search_prime/mcp", Description: "Z.ai web search (GLM Coding Plan)"},
		{Suffix: "web-reader", URL: root + "/mcp/web_reader/mcp", Description: "Z.ai web page reader (GLM Coding Plan)"},
		{Suffix: "zread", URL: root + "/mcp/zread/mcp", Description: "Z.ai zread: public GitHub repository docs and files (GLM Coding Plan)"},
	}
}

// paasSuffix matches the chat API part of a Z.ai base URL path:
// "/paas/v4" or "/coding/paas/v4", after the "/api" root.
var paasSuffix = regexp.MustCompile(`/(coding/)?paas/v[0-9]+$`)

// apiRoot returns the "scheme://host[/prefix]/api" root of a Z.ai base URL
// such as "https://api.z.ai/api/paas/v4" or ".../api/coding/paas/v4". Only the
// known chat API suffix is removed, so a proxy prefix, even one containing
// "/api/", is kept. A path without that suffix falls back to the host's "/api".
func apiRoot(baseURL string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false
	}
	root := "/api"
	if path := strings.TrimRight(u.Path, "/"); paasSuffix.MatchString(path) {
		root = paasSuffix.ReplaceAllString(path, "")
	}
	return u.Scheme + "://" + u.Host + root, true
}
