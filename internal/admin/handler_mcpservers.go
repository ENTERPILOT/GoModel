package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/mcpgateway"
)

// MCPServerAdmin is the narrow surface of *mcpgateway.Service the admin API
// needs, kept as an interface so handler tests can stub the gateway without
// dialing upstream servers.
type MCPServerAdmin interface {
	Views() []mcpgateway.ServerView
	IsManaged(name string) bool
	GetManaged(ctx context.Context, name string) (*mcpgateway.ManagedServer, error)
	Upsert(ctx context.Context, server mcpgateway.ManagedServer) error
	Delete(ctx context.Context, name string) error
	Reconnect(ctx context.Context, name string) (mcpgateway.ServerView, error)
	Catalog(name string) (mcpgateway.CatalogView, bool)
}

// redactedMCPHeaderValue replaces upstream header values (the credential
// boundary) in admin views. An upsert that sends the placeholder back keeps
// the currently stored value, so the dashboard can round-trip a server
// definition without ever seeing its secrets.
const redactedMCPHeaderValue = "***"

// upsertMCPServerRequest is the admin upsert contract for one MCP server.
// Header values equal to "***" preserve the stored value; enabled defaults to
// true, preserving the existing value when omitted on an update.
type upsertMCPServerRequest struct {
	Name               string            `json:"name"`
	Slug               string            `json:"slug,omitempty"`
	URL                string            `json:"url"`
	Transport          string            `json:"transport,omitempty"`
	Headers            map[string]string `json:"headers,omitempty"`
	Description        string            `json:"description,omitempty"`
	Enabled            *bool             `json:"enabled,omitempty"`
	AllowedTools       []string          `json:"allowed_tools,omitempty"`
	DisallowedTools    []string          `json:"disallowed_tools,omitempty"`
	UserPaths          []string          `json:"user_paths,omitempty"`
	ToolTimeoutSeconds int               `json:"tool_timeout_seconds,omitempty"`
}

// mcpServerViewResponse is the admin view of one MCP server: its definition
// (headers redacted) plus runtime connection state. Managed marks
// config/env-declared servers, which are read-only in the dashboard.
type mcpServerViewResponse struct {
	Name               string            `json:"name"`
	Slug               string            `json:"slug"`
	URL                string            `json:"url"`
	Transport          string            `json:"transport"`
	Description        string            `json:"description,omitempty"`
	Enabled            bool              `json:"enabled"`
	AllowedTools       []string          `json:"allowed_tools,omitempty"`
	DisallowedTools    []string          `json:"disallowed_tools,omitempty"`
	UserPaths          []string          `json:"user_paths,omitempty"`
	ToolTimeoutSeconds int               `json:"tool_timeout_seconds,omitempty"`
	Headers            map[string]string `json:"headers,omitempty"`
	Managed            bool              `json:"managed"`
	Status             string            `json:"status"`
	LastError          string            `json:"last_error,omitempty"`
	ToolCount          int               `json:"tool_count"`
	PromptCount        int               `json:"prompt_count"`
	ResourceCount      int               `json:"resource_count"`
	ConnectedAt        *time.Time        `json:"connected_at,omitempty"`
}

// ListMCPServers handles GET /admin/mcp-servers.
//
// @Summary      List MCP servers (config-declared and admin-managed)
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Success      200  {array}   mcpServerViewResponse
// @Failure      401  {object}  core.GatewayError
// @Failure      503  {object}  core.GatewayError
// @Router       /admin/mcp-servers [get]
func (h *Handler) ListMCPServers(c *echo.Context) error {
	if h.mcpServers == nil {
		return handleError(c, featureUnavailableError("mcp gateway feature is unavailable"))
	}
	views := h.mcpServers.Views()
	result := make([]mcpServerViewResponse, 0, len(views))
	for _, view := range views {
		result = append(result, h.mcpServerView(view))
	}
	return c.JSON(http.StatusOK, result)
}

// UpsertMCPServer handles PUT /admin/mcp-servers.
//
// @Summary      Create or update one admin-managed MCP server
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        mcp_server  body      upsertMCPServerRequest  true  "MCP server definition"
// @Success      200         {object}  mcpServerViewResponse
// @Failure      400         {object}  core.GatewayError
// @Failure      401         {object}  core.GatewayError
// @Failure      502         {object}  core.GatewayError
// @Failure      503         {object}  core.GatewayError
// @Router       /admin/mcp-servers [put]
func (h *Handler) UpsertMCPServer(c *echo.Context) error {
	if h.mcpServers == nil {
		return handleError(c, featureUnavailableError("mcp gateway feature is unavailable"))
	}

	var req upsertMCPServerRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	displayName := strings.TrimSpace(req.Name)
	if displayName == "" {
		return handleError(c, core.NewInvalidRequestError("name is required", nil))
	}
	slug := strings.ToLower(strings.TrimSpace(req.Slug))
	if slug == "" {
		slug = config.DeriveMCPServerSlug(displayName)
	}
	if h.mcpServers.IsManaged(slug) {
		return handleError(c, core.NewInvalidRequestError("mcp server "+slug+" is managed by config/env and is read-only", nil))
	}

	server, err := h.buildMCPServerUpsert(c.Request().Context(), slug, displayName, req)
	if err != nil {
		return handleError(c, err)
	}
	if err := h.mcpServers.Upsert(c.Request().Context(), server); err != nil {
		return handleError(c, mcpServerWriteError(err))
	}

	if view, ok := h.findMCPServerView(server.Name); ok {
		return c.JSON(http.StatusOK, view)
	}
	return c.NoContent(http.StatusNoContent)
}

// DeleteMCPServer handles DELETE /admin/mcp-servers/:name.
//
// @Summary      Delete one admin-managed MCP server
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        name  path  string  true  "MCP server slug"
// @Success      204   "No Content"
// @Failure      400   {object}  core.GatewayError
// @Failure      401   {object}  core.GatewayError
// @Failure      404   {object}  core.GatewayError
// @Failure      502   {object}  core.GatewayError
// @Failure      503   {object}  core.GatewayError
// @Router       /admin/mcp-servers/{name} [delete]
func (h *Handler) DeleteMCPServer(c *echo.Context) error {
	if h.mcpServers == nil {
		return handleError(c, featureUnavailableError("mcp gateway feature is unavailable"))
	}

	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		return handleError(c, core.NewInvalidRequestError("name is required", nil))
	}
	if h.mcpServers.IsManaged(name) {
		return handleError(c, core.NewInvalidRequestError("mcp server "+name+" is managed by config/env and is read-only", nil))
	}

	if err := h.mcpServers.Delete(c.Request().Context(), name); err != nil {
		if errors.Is(err, mcpgateway.ErrNotFound) {
			return handleError(c, core.NewNotFoundError("mcp server not found: "+name))
		}
		return handleError(c, mcpServerWriteError(err))
	}
	return c.NoContent(http.StatusNoContent)
}

// ReconnectMCPServer handles POST /admin/mcp-servers/:name/reconnect. The
// action succeeds even when the upstream stays down: the refreshed view then
// carries the degraded status and last_error. Only an unknown name is a 404.
//
// @Summary      Force-redial one MCP server and return its fresh state
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        name  path  string  true  "MCP server slug"
// @Success      200   {object}  mcpServerViewResponse
// @Failure      400   {object}  core.GatewayError
// @Failure      401   {object}  core.GatewayError
// @Failure      404   {object}  core.GatewayError
// @Failure      503   {object}  core.GatewayError
// @Router       /admin/mcp-servers/{name}/reconnect [post]
func (h *Handler) ReconnectMCPServer(c *echo.Context) error {
	if h.mcpServers == nil {
		return handleError(c, featureUnavailableError("mcp gateway feature is unavailable"))
	}

	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		return handleError(c, core.NewInvalidRequestError("name is required", nil))
	}

	view, err := h.mcpServers.Reconnect(c.Request().Context(), name)
	if err != nil && view.Spec.Name == "" {
		// An empty view means the name is not configured; a populated view means
		// the redial ran but the upstream failed, which the view itself reports.
		return handleError(c, core.NewNotFoundError("mcp server not found: "+name))
	}
	return c.JSON(http.StatusOK, h.mcpServerView(view))
}

// MCPServerCatalog handles GET /admin/mcp-servers/:name/catalog.
//
// @Summary      Inspect one MCP server's current catalog
// @Description  Lists the tools, prompts, resources, and resource templates the named server currently exposes through the gateway, after operator tool filters. Names are the upstream originals; the aggregated /mcp endpoint prefixes them with the server slug.
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        name  path  string  true  "MCP server slug"
// @Success      200   {object}  mcpgateway.CatalogView
// @Failure      401   {object}  core.GatewayError
// @Failure      404   {object}  core.GatewayError
// @Failure      503   {object}  core.GatewayError
// @Router       /admin/mcp-servers/{name}/catalog [get]
func (h *Handler) MCPServerCatalog(c *echo.Context) error {
	if h.mcpServers == nil {
		return handleError(c, featureUnavailableError("mcp gateway feature is unavailable"))
	}
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		return handleError(c, core.NewInvalidRequestError("name is required", nil))
	}
	catalog, ok := h.mcpServers.Catalog(name)
	if !ok {
		return handleError(c, core.NewNotFoundError("mcp server not found: "+name))
	}
	return c.JSON(http.StatusOK, catalog)
}

// buildMCPServerUpsert maps the request onto a validated ManagedServer,
// resolving "***" header placeholders against the stored row.
func (h *Handler) buildMCPServerUpsert(ctx context.Context, slug, displayName string, req upsertMCPServerRequest) (mcpgateway.ManagedServer, error) {
	current, err := h.mcpServers.GetManaged(ctx, slug)
	if err != nil && !errors.Is(err, mcpgateway.ErrNotFound) {
		return mcpgateway.ManagedServer{}, mcpServerWriteError(err)
	}

	headers, err := mergeRedactedMCPHeaders(req.Headers, current)
	if err != nil {
		return mcpgateway.ManagedServer{}, err
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	} else if current != nil {
		enabled = current.Enabled
	}

	server := mcpgateway.ManagedServer{
		Name:               slug,
		DisplayName:        displayName,
		URL:                strings.TrimSpace(req.URL),
		Transport:          strings.TrimSpace(req.Transport),
		Headers:            headers,
		Description:        strings.TrimSpace(req.Description),
		Enabled:            enabled,
		AllowedTools:       req.AllowedTools,
		DisallowedTools:    req.DisallowedTools,
		UserPaths:          req.UserPaths,
		ToolTimeoutSeconds: req.ToolTimeoutSeconds,
	}
	if current != nil {
		server.CreatedAt = current.CreatedAt
	}
	if err := server.Validate(); err != nil {
		return mcpgateway.ManagedServer{}, core.NewInvalidRequestError(err.Error(), err)
	}
	return server, nil
}

// mergeRedactedMCPHeaders resolves "***" placeholders to the stored header
// values. A placeholder with no stored counterpart is rejected rather than
// persisted, so the literal placeholder can never become a real credential.
func mergeRedactedMCPHeaders(incoming map[string]string, current *mcpgateway.ManagedServer) (map[string]string, error) {
	if len(incoming) == 0 {
		return nil, nil
	}
	merged := make(map[string]string, len(incoming))
	for name, value := range incoming {
		if value != redactedMCPHeaderValue {
			merged[name] = value
			continue
		}
		if current != nil {
			if stored, ok := current.Headers[name]; ok {
				merged[name] = stored
				continue
			}
		}
		return nil, core.NewInvalidRequestError("header "+name+" is redacted but has no stored value to preserve; provide the real value", nil)
	}
	return merged, nil
}

// mcpServerView converts one gateway snapshot into the admin response shape,
// redacting header values.
func (h *Handler) mcpServerView(view mcpgateway.ServerView) mcpServerViewResponse {
	spec := view.Spec
	displayName := strings.TrimSpace(spec.DisplayName)
	if displayName == "" {
		displayName = spec.Name
	}
	resp := mcpServerViewResponse{
		Name:               displayName,
		Slug:               spec.Name,
		URL:                spec.URL,
		Transport:          spec.Transport,
		Description:        spec.Description,
		Enabled:            spec.Enabled,
		AllowedTools:       spec.AllowedTools,
		DisallowedTools:    spec.DisallowedTools,
		UserPaths:          spec.UserPaths,
		ToolTimeoutSeconds: int(spec.ToolTimeout / time.Second),
		Headers:            redactMCPHeaders(spec.Headers),
		Managed:            h.mcpServers.IsManaged(spec.Name),
		Status:             string(view.Status),
		LastError:          view.LastError,
		ToolCount:          view.ToolCount,
		PromptCount:        view.PromptCount,
		ResourceCount:      view.ResourceCount,
	}
	if !view.ConnectedAt.IsZero() {
		connectedAt := view.ConnectedAt
		resp.ConnectedAt = &connectedAt
	}
	return resp
}

// redactMCPHeaders keeps header names but replaces every value, so views can
// show which headers are configured without exposing upstream credentials.
func redactMCPHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	redacted := make(map[string]string, len(headers))
	for name := range headers {
		redacted[name] = redactedMCPHeaderValue
	}
	return redacted
}

// findMCPServerView returns the admin view for a name after an upsert by
// matching it in the refreshed listing.
func (h *Handler) findMCPServerView(name string) (mcpServerViewResponse, bool) {
	for _, view := range h.mcpServers.Views() {
		if view.Spec.Name == name {
			return h.mcpServerView(view), true
		}
	}
	return mcpServerViewResponse{}, false
}

// mcpServerWriteError surfaces gateway store/reload failures as 502, mirroring
// virtualModelWriteError. Validation runs in the handler before the service
// call, so remaining errors are infrastructure failures, not input issues.
func mcpServerWriteError(err error) error {
	if err == nil {
		return nil
	}
	return core.NewProviderError("mcp_servers", http.StatusBadGateway, err.Error(), err)
}
