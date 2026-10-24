package zai

import (
	"net/url"
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

// apiRoot returns the "scheme://host[/prefix]/api" root of a Z.ai base URL
// such as "https://api.z.ai/api/paas/v4" or ".../api/coding/paas/v4".
func apiRoot(baseURL string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false
	}
	prefix := ""
	if i := strings.Index(u.Path+"/", "/api/"); i >= 0 {
		prefix = u.Path[:i]
	}
	return u.Scheme + "://" + u.Host + prefix + "/api", true
}
