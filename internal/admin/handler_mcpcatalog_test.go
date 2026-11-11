package admin

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/mcpgateway"
)

func TestMCPToolCatalog_Unavailable(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := echotest.Get(t, "/admin/mcp-servers/catalog")
	require.NoError(t, h.MCPToolCatalog(c))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
}

func TestMCPToolCatalog_Params(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		toolErr    error
		wantStatus int
		wantFilter mcpgateway.ToolCatalogFilter
	}{
		{name: "no filters", wantStatus: http.StatusOK},
		{name: "json format", query: "format=JSON", wantStatus: http.StatusOK},
		{name: "by server", query: "server=+github+", wantStatus: http.StatusOK, wantFilter: mcpgateway.ToolCatalogFilter{Server: "github"}},
		{name: "by virtual server", query: "virtual_server=coding", wantStatus: http.StatusOK, wantFilter: mcpgateway.ToolCatalogFilter{VirtualServer: "coding"}},
		{name: "as seen by a user path", query: "user_path=team/x/", wantStatus: http.StatusOK, wantFilter: mcpgateway.ToolCatalogFilter{UserPath: "/team/x"}},
		{
			name: "filters combine", query: "server=github&virtual_server=coding&user_path=/team/x&format=csv", wantStatus: http.StatusOK,
			wantFilter: mcpgateway.ToolCatalogFilter{Server: "github", VirtualServer: "coding", UserPath: "/team/x"},
		},
		{name: "unknown format", query: "format=xml", wantStatus: http.StatusBadRequest},
		{name: "invalid user path", query: "user_path=/team/../x", wantStatus: http.StatusBadRequest},
		{
			name: "unknown server or virtual server", query: "server=ghost", wantStatus: http.StatusNotFound,
			toolErr:    fmt.Errorf("%w: ghost", mcpgateway.ErrNotFound),
			wantFilter: mcpgateway.ToolCatalogFilter{Server: "ghost"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newMCPAdminFake()
			fake.toolErr = tt.toolErr
			c, rec := echotest.Get(t, "/admin/mcp-servers/catalog?"+tt.query)
			require.NoError(t, newMCPHandler(fake).MCPToolCatalog(c))
			assert.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			assert.Equal(t, tt.wantFilter, fake.toolFilter)
		})
	}
}

func TestMCPToolCatalog_JSON(t *testing.T) {
	fake := newMCPAdminFake()
	github := mcpgateway.ToolCatalogServer{
		Server: "github", ServerName: "GitHub", Transport: "http", Enabled: true,
		Status: mcpgateway.StatusConnected, UserPaths: []string{"/team/x"}, DisallowedUserPaths: []string{},
	}
	fake.toolCatalog = mcpgateway.ToolCatalog{
		Servers: []mcpgateway.ToolCatalogServer{github},
		Tools: []mcpgateway.ToolCatalogTool{
			{ToolCatalogServer: github, Name: "github_get_issue", UpstreamName: "get_issue", Description: "Get an issue", ReadOnly: true, Exposed: true},
		},
	}

	c, rec := echotest.Get(t, "/admin/mcp-servers/catalog")
	require.NoError(t, newMCPHandler(fake).MCPToolCatalog(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{
		"servers": [{
			"server": "github", "server_name": "GitHub", "transport": "http", "enabled": true, "status": "connected",
			"user_paths": ["/team/x"], "disallowed_user_paths": []
		}],
		"tools": [{
			"server": "github", "server_name": "GitHub", "transport": "http", "enabled": true, "status": "connected",
			"user_paths": ["/team/x"], "disallowed_user_paths": [],
			"name": "github_get_issue", "upstream_name": "get_issue", "description": "Get an issue",
			"read_only": true, "destructive": false, "exposed": true
		}]
	}`, rec.Body.String())
}

func TestMCPToolCatalog_CSV(t *testing.T) {
	github := mcpgateway.ToolCatalogServer{
		Server: "github", ServerName: "=GitHub", Transport: "http", Enabled: true,
		Status: mcpgateway.StatusConnected, UserPaths: []string{"/team/x", "/team/y"}, DisallowedUserPaths: []string{"/team/x/guests"},
	}
	down := mcpgateway.ToolCatalogServer{
		Server: "down", ServerName: "down", Transport: "sse", Enabled: true,
		Status: mcpgateway.StatusDegraded, UserPaths: []string{}, DisallowedUserPaths: []string{},
	}
	fake := newMCPAdminFake()
	fake.toolCatalog = mcpgateway.ToolCatalog{
		Servers: []mcpgateway.ToolCatalogServer{down, github},
		Tools: []mcpgateway.ToolCatalogTool{
			{ToolCatalogServer: github, Name: "github_get_issue", UpstreamName: "get_issue", Description: "Get an issue, \"quoted\"\nsecond line", ReadOnly: true, Exposed: true},
			{ToolCatalogServer: github, UpstreamName: "=HYPERLINK(\"http://evil\")", Description: "@SUM(1+1)", Destructive: true},
			{ToolCatalogServer: github, Name: "github_+1", UpstreamName: "+1", Description: "-2+3", Exposed: true},
			{ToolCatalogServer: github, Name: "github_tab", UpstreamName: "\tcmd", Description: "\r=1", Exposed: true},
		},
	}

	c, rec := echotest.Get(t, "/admin/mcp-servers/catalog?format=csv")
	require.NoError(t, newMCPHandler(fake).MCPToolCatalog(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "text/csv; charset=utf-8", rec.Header().Get(echo.HeaderContentType))
	assert.Equal(t, `attachment; filename="gomodel-mcp-tools.csv"`, rec.Header().Get(echo.HeaderContentDisposition))

	// Quotes are doubled and fields with commas, quotes, or line breaks quoted.
	assert.Contains(t, rec.Body.String(), `"Get an issue, ""quoted""`+"\nsecond line\"")

	records, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	require.NoError(t, err)
	githubColumns := []string{"github", "'=GitHub", "http", "true", "connected"}
	githubPaths := []string{"/team/x\n/team/y", "/team/x/guests"}
	row := func(tool ...string) []string {
		return append(append(append([]string{}, githubColumns...), tool...), githubPaths...)
	}
	assert.Equal(t, [][]string{
		{
			"server", "server_name", "transport", "enabled", "status",
			"name", "upstream_name", "description", "read_only", "destructive", "exposed",
			"user_paths", "disallowed_user_paths",
		},
		row("github_get_issue", "get_issue", "Get an issue, \"quoted\"\nsecond line", "true", "false", "true"),
		// Values a spreadsheet would run as a formula get a leading quote.
		row("", `'=HYPERLINK("http://evil")`, "'@SUM(1+1)", "false", "true", "false"),
		row("github_+1", "'+1", "'-2+3", "false", "false", "true"),
		row("github_tab", "'\tcmd", "'\r=1", "false", "false", "true"),
		// A server without tools still gets a row, after the tool rows.
		{"down", "down", "sse", "true", "degraded", "", "", "", "", "", "", "", ""},
	}, records)
}
