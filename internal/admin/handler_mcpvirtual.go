package admin

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/mcpgateway"
)

// mcpVirtualServerResponse is the admin view of one virtual MCP server,
// served at /mcp/{name}. Managed marks config/env-declared entries, which are
// read-only. Conflict is set when a server with the same slug keeps that
// path, so the virtual server is not served.
type mcpVirtualServerResponse struct {
	Name           string   `json:"name"`
	Description    string   `json:"description,omitempty"`
	Servers        []string `json:"servers"`
	MissingServers []string `json:"missing_servers,omitempty"`
	ToolDiscovery  string   `json:"tool_discovery"`
	// ConfiguredToolDiscovery is the stored setting: "off", "search", or ""
	// to inherit the gateway default. ToolDiscovery is the effective mode.
	ConfiguredToolDiscovery string `json:"configured_tool_discovery,omitempty"`
	Managed                 bool   `json:"managed"`
	Conflict                string `json:"conflict,omitempty"`
}

// upsertMCPVirtualServerRequest is the admin upsert contract for one virtual
// server.
type upsertMCPVirtualServerRequest struct {
	Name          string   `json:"name"`
	Description   string   `json:"description,omitempty"`
	Servers       []string `json:"servers"`
	ToolDiscovery string   `json:"tool_discovery,omitempty"`
}

// ListMCPVirtualServers handles GET /admin/mcp-virtual-servers.
//
// @Summary      List virtual MCP servers (config-declared and admin-managed)
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
		result = append(result, mcpVirtualServerView(view))
	}
	return c.JSON(http.StatusOK, result)
}

// UpsertMCPVirtualServer handles PUT /admin/mcp-virtual-servers.
//
// @Summary      Create or update one admin-managed virtual MCP server
// @Description  Members must be existing MCP servers. A new virtual server may not use an MCP server's slug, and config-declared virtual servers are read-only.
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        virtual_server  body      upsertMCPVirtualServerRequest  true  "Virtual MCP server definition"
// @Success      200             {object}  mcpVirtualServerResponse
// @Failure      400             {object}  core.GatewayError
// @Failure      401             {object}  core.GatewayError
// @Failure      502             {object}  core.GatewayError
// @Failure      503             {object}  core.GatewayError
// @Router       /admin/mcp-virtual-servers [put]
func (h *Handler) UpsertMCPVirtualServer(c *echo.Context) error {
	if h.mcpServers == nil {
		return handleError(c, featureUnavailableError("mcp gateway feature is unavailable"))
	}
	var req upsertMCPVirtualServerRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	virtual := mcpgateway.ManagedVirtualServer{
		Name:          strings.ToLower(strings.TrimSpace(req.Name)),
		Description:   req.Description,
		Servers:       req.Servers,
		ToolDiscovery: req.ToolDiscovery,
	}
	view, err := h.mcpServers.UpsertVirtual(c.Request().Context(), virtual)
	if err != nil {
		return handleError(c, mcpVirtualServerWriteError(err))
	}
	return c.JSON(http.StatusOK, mcpVirtualServerView(view))
}

// DeleteMCPVirtualServer handles DELETE /admin/mcp-virtual-servers/:name.
//
// @Summary      Delete one admin-managed virtual MCP server
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        name  path  string  true  "Virtual MCP server name"
// @Success      204   "No Content"
// @Failure      400   {object}  core.GatewayError
// @Failure      401   {object}  core.GatewayError
// @Failure      404   {object}  core.GatewayError
// @Failure      502   {object}  core.GatewayError
// @Failure      503   {object}  core.GatewayError
// @Router       /admin/mcp-virtual-servers/{name} [delete]
func (h *Handler) DeleteMCPVirtualServer(c *echo.Context) error {
	if h.mcpServers == nil {
		return handleError(c, featureUnavailableError("mcp gateway feature is unavailable"))
	}
	return deleteManagedResource(c, "virtual mcp server", h.mcpServers.IsManagedVirtual, h.mcpServers.DeleteVirtual, mcpgateway.ErrNotFound, mcpVirtualServerWriteError)
}

func mcpVirtualServerView(view mcpgateway.VirtualServerView) mcpVirtualServerResponse {
	return mcpVirtualServerResponse{
		Name:                    view.Spec.Name,
		Description:             view.Spec.Description,
		Servers:                 slices.Clone(view.Spec.Servers),
		MissingServers:          slices.Clone(view.MissingServers),
		ToolDiscovery:           view.ToolDiscovery,
		ConfiguredToolDiscovery: view.Spec.ToolDiscovery,
		Managed:                 view.Spec.Managed,
		Conflict:                view.Conflict,
	}
}

// mcpVirtualServerWriteError maps refused definitions to 400 and store or
// reload failures to 502, mirroring mcpServerWriteError.
func mcpVirtualServerWriteError(err error) error {
	if errors.Is(err, mcpgateway.ErrInvalidVirtualServer) {
		return core.NewInvalidRequestError(err.Error(), err)
	}
	return core.NewProviderError("mcp_virtual_servers", http.StatusBadGateway, err.Error(), err)
}
