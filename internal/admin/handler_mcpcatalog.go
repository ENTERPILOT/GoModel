package admin

import (
	"bytes"
	"encoding/csv"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/mcpgateway"
)

// mcpToolCatalogCSVHeader is the stable column order of the CSV tool catalog,
// named after the JSON fields. New columns go at the end, so positional
// readers keep working.
var mcpToolCatalogCSVHeader = []string{
	"server", "server_name", "transport", "enabled", "status",
	"name", "upstream_name", "description", "read_only", "destructive", "exposed", "shadowed_by",
	"user_paths", "disallowed_user_paths",
}

// MCPToolCatalog handles GET /admin/mcp-servers/catalog.
//
// @Summary      Export every MCP tool across all servers
// @Description  Lists every tool the MCP servers report, one row per tool with its server, for security reviews and audits. name is the name that reaches the tool on the aggregated /mcp endpoint and upstream_name the server's own name, which /mcp/{server} serves. exposed is false for a tool the server's allowed_tools / disallowed_tools hide. shadowed_by names the server that owns the tool's /mcp name when two servers produce the same one: the gateway keeps the first in slug order, so that name reaches the other server's tool, and this one stays callable only on /mcp/{server}. name is empty for a tool that is not exposed or is shadowed. read_only and destructive are true only when the upstream set the hint. Each row repeats its server's transport, enabled flag, status, and user-path scope. virtual_server and user_path describe the endpoint: they narrow the servers, combine, and decide name ownership among the servers left; without them ownership is decided across all servers. An empty user_path does not filter. server only keeps that server's rows. The export reads the catalogs the gateway already holds: a disabled, connecting, or unreachable server is listed under servers with its status and no tools, and a degraded server keeps its last catalog. In CSV, a server without tools is one row with empty tool columns, and user paths are separated by line breaks.
// @Tags         admin
// @Produce      json
// @Produce      text/csv
// @Security     BearerAuth
// @Param        format          query     string  false  "Export format: json (default) or csv (as an attachment)"
// @Param        server          query     string  false  "Keep the rows of one MCP server, by slug"
// @Param        virtual_server  query     string  false  "Keep the members of one virtual MCP server"
// @Param        user_path       query     string  false  "Keep the servers a caller with this user path can see, e.g. /team/x"
// @Success      200  {object}  mcpgateway.ToolCatalog
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Failure      403  {object}  core.GatewayError
// @Failure      404  {object}  core.GatewayError
// @Failure      503  {object}  core.GatewayError
// @Router       /admin/mcp-servers/catalog [get]
func (h *Handler) MCPToolCatalog(c *echo.Context) error {
	if h.mcpServers == nil {
		return handleError(c, featureUnavailableError("mcp gateway feature is unavailable"))
	}
	format := strings.ToLower(strings.TrimSpace(c.QueryParam("format")))
	if format != "" && format != "json" && format != "csv" {
		return handleError(c, core.NewInvalidRequestError("invalid format: use json or csv", nil))
	}
	userPath, err := normalizeUserPathQueryParam("user_path", c.QueryParam("user_path"))
	if err != nil {
		return handleError(c, err)
	}
	catalog, err := h.mcpServers.ToolCatalog(mcpgateway.ToolCatalogFilter{
		Server:        strings.TrimSpace(c.QueryParam("server")),
		VirtualServer: strings.TrimSpace(c.QueryParam("virtual_server")),
		UserPath:      userPath,
	})
	if errors.Is(err, mcpgateway.ErrNotFound) {
		return handleError(c, core.NewNotFoundError(err.Error()))
	}
	if err != nil {
		return handleError(c, err)
	}

	if format != "csv" {
		return c.JSON(http.StatusOK, catalog)
	}
	body, err := mcpToolCatalogCSV(catalog)
	if err != nil {
		return handleError(c, err)
	}
	c.Response().Header().Set(echo.HeaderContentDisposition, `attachment; filename="gomodel-mcp-tools.csv"`)
	return c.Blob(http.StatusOK, "text/csv; charset=utf-8", body)
}

// mcpToolCatalogCSV renders one row per tool, then one row with empty tool
// columns for each server that lists no tools, so every server is on file.
func mcpToolCatalogCSV(catalog mcpgateway.ToolCatalog) ([]byte, error) {
	records := [][]string{mcpToolCatalogCSVHeader}
	listed := make(map[string]bool, len(catalog.Servers))
	for _, tool := range catalog.Tools {
		listed[tool.Server] = true
		records = append(records, mcpToolCatalogRecord(tool.ToolCatalogServer, &tool))
	}
	for _, server := range catalog.Servers {
		if !listed[server.Server] {
			records = append(records, mcpToolCatalogRecord(server, nil))
		}
	}
	var buf bytes.Buffer
	if err := csv.NewWriter(&buf).WriteAll(records); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// mcpToolCatalogRecord is one CSV row; a nil tool leaves the tool columns
// empty. Names and descriptions come from upstream servers, so they pass
// through csvText; user paths start with "/" and need no guard.
func mcpToolCatalogRecord(server mcpgateway.ToolCatalogServer, tool *mcpgateway.ToolCatalogTool) []string {
	toolColumns := make([]string, 7)
	if tool != nil {
		toolColumns = []string{
			csvText(tool.Name), csvText(tool.UpstreamName), csvText(tool.Description),
			strconv.FormatBool(tool.ReadOnly), strconv.FormatBool(tool.Destructive), strconv.FormatBool(tool.Exposed),
			csvText(tool.ShadowedBy),
		}
	}
	record := []string{
		csvText(server.Server), csvText(server.ServerName), csvText(server.Transport),
		strconv.FormatBool(server.Enabled), string(server.Status),
	}
	record = append(record, toolColumns...)
	return append(record, strings.Join(server.UserPaths, "\n"), strings.Join(server.DisallowedUserPaths, "\n"))
}
