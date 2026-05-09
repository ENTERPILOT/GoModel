package admin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/aliases"
	"gomodel/internal/core"
)

type upsertAliasRequest struct {
	Name           string `json:"name"`
	TargetModel    string `json:"target_model"`
	TargetProvider string `json:"target_provider,omitempty"`
	Description    string `json:"description,omitempty"`
	Enabled        *bool  `json:"enabled,omitempty"`
}

type deleteAliasRequest struct {
	Name string `json:"name"`
}

func (h *Handler) ListAliases(c *echo.Context) error {
	if h.aliases == nil {
		return handleError(c, featureUnavailableError("aliases feature is unavailable"))
	}
	views := h.aliases.ListViews()
	if views == nil {
		views = []aliases.View{}
	}
	return c.JSON(http.StatusOK, views)
}

// UpsertAlias handles PUT /admin/aliases
func (h *Handler) UpsertAlias(c *echo.Context) error {
	if h.aliases == nil {
		return handleError(c, featureUnavailableError("aliases feature is unavailable"))
	}

	var req upsertAliasRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return handleError(c, core.NewInvalidRequestError("alias name is required", nil))
	}

	enabled := true
	if existing, ok := h.aliases.Get(name); ok && existing != nil {
		enabled = existing.Enabled
	}
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	if err := h.aliases.Upsert(c.Request().Context(), aliases.Alias{
		Name:           name,
		TargetModel:    req.TargetModel,
		TargetProvider: req.TargetProvider,
		Description:    req.Description,
		Enabled:        enabled,
	}); err != nil {
		return handleError(c, aliasWriteError(err))
	}

	alias, ok := h.aliases.Get(name)
	if !ok {
		return c.NoContent(http.StatusNoContent)
	}
	return c.JSON(http.StatusOK, alias)
}

// DeleteAlias handles DELETE /admin/aliases
func (h *Handler) DeleteAlias(c *echo.Context) error {
	if h.aliases == nil {
		return handleError(c, featureUnavailableError("aliases feature is unavailable"))
	}

	var req deleteAliasRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return handleError(c, core.NewInvalidRequestError("alias name is required", nil))
	}

	if err := h.aliases.Delete(c.Request().Context(), name); err != nil {
		if errors.Is(err, aliases.ErrNotFound) {
			return handleError(c, core.NewNotFoundError("alias not found: "+name))
		}
		return handleError(c, aliasWriteError(err))
	}
	return c.NoContent(http.StatusNoContent)
}
