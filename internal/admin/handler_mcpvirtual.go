package admin

import (
	"net/http"
	"slices"

	"github.com/labstack/echo/v5"
)

// mcpVirtualServerResponse is the admin view of one config-declared virtual
// MCP server, served at /mcp/{name}. Conflict is set when a server with the
// same slug keeps that path, so the virtual server is not served.
type mcpVirtualServerResponse struct {
	Name           string   `json:"name"`
	Description    string   `json:"description,omitempty"`
	Servers        []string `json:"servers"`
	MissingServers []string `json:"missing_servers,omitempty"`
	ToolDiscovery  string   `json:"tool_discovery"`
	Conflict       string   `json:"conflict,omitempty"`
}

// ListMCPVirtualServers handles GET /admin/mcp-virtual-servers.
//
// @Summary      List virtual MCP servers (config-declared, read-only)
// @Description  Each virtual server serves a subset of the MCP servers at /mcp/{name}. missing_servers lists members no server matches; conflict explains why a virtual server is not served.
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Success      200  {array}   mcpVirtualServerResponse
// @Failure      401  {object}  core.GatewayError
// @Failure      503  {object}  core.GatewayError
// @Router       /admin/mcp-virtual-servers [get]
func (h *Handler) ListMCPVirtualServers(c *echo.Context) error {
	if h.mcpServers == nil {
		return handleError(c, featureUnavailableError("mcp gateway feature is unavailable"))
	}
	views := h.mcpServers.VirtualViews()
	result := make([]mcpVirtualServerResponse, 0, len(views))
	for _, view := range views {
		result = append(result, mcpVirtualServerResponse{
			Name:           view.Spec.Name,
			Description:    view.Spec.Description,
			Servers:        slices.Clone(view.Spec.Servers),
			MissingServers: slices.Clone(view.MissingServers),
			ToolDiscovery:  view.ToolDiscovery,
			Conflict:       view.Conflict,
		})
	}
	return c.JSON(http.StatusOK, result)
}
