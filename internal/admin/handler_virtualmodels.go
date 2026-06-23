package admin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
	"gomodel/internal/virtualmodels"
)

// upsertVirtualModelRequest is the unified admin upsert contract. Presence of
// target_model makes the row a redirect; absence makes it an access policy.
type upsertVirtualModelRequest struct {
	Source      string   `json:"source"`
	TargetModel string   `json:"target_model,omitempty"`
	UserPaths   []string `json:"user_paths,omitempty"`
	Description string   `json:"description,omitempty"`
	Enabled     *bool    `json:"enabled,omitempty"`
}

type deleteVirtualModelRequest struct {
	Source string `json:"source"`
}

// ListVirtualModels handles GET /admin/virtual-models.
//
// @Summary      List virtual models (redirects and access policies)
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Success      200  {array}   virtualmodels.View
// @Failure      401  {object}  core.GatewayError
// @Failure      503  {object}  core.GatewayError
// @Router       /admin/virtual-models [get]
func (h *Handler) ListVirtualModels(c *echo.Context) error {
	if h.virtualModels == nil {
		return handleError(c, featureUnavailableError("virtual models feature is unavailable"))
	}
	views := h.virtualModels.ListViews()
	if views == nil {
		views = []virtualmodels.View{}
	}
	return c.JSON(http.StatusOK, views)
}

// UpsertVirtualModel handles PUT /admin/virtual-models.
//
// @Summary      Create or update one virtual model
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        virtual_model  body      upsertVirtualModelRequest  true  "Virtual model definition"
// @Success      200            {object}  virtualmodels.View
// @Failure      400            {object}  core.GatewayError
// @Failure      401            {object}  core.GatewayError
// @Failure      500            {object}  core.GatewayError
// @Failure      503            {object}  core.GatewayError
// @Router       /admin/virtual-models [put]
func (h *Handler) UpsertVirtualModel(c *echo.Context) error {
	if h.virtualModels == nil {
		return handleError(c, featureUnavailableError("virtual models feature is unavailable"))
	}

	var req upsertVirtualModelRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	source := strings.TrimSpace(req.Source)
	if source == "" {
		return handleError(c, core.NewInvalidRequestError("source is required", nil))
	}

	vm, err := h.buildVirtualModelUpsert(source, req)
	if err != nil {
		return handleError(c, err)
	}
	if err := h.virtualModels.Upsert(c.Request().Context(), vm); err != nil {
		return handleError(c, virtualModelWriteError(err))
	}

	if view, ok := h.findVirtualModelView(vm.Source); ok {
		return c.JSON(http.StatusOK, view)
	}
	return c.NoContent(http.StatusNoContent)
}

// DeleteVirtualModel handles DELETE /admin/virtual-models.
//
// @Summary      Delete one virtual model
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body  deleteVirtualModelRequest  true  "Virtual model source to remove"
// @Success      204       "No Content"
// @Failure      400       {object}  core.GatewayError
// @Failure      401       {object}  core.GatewayError
// @Failure      404       {object}  core.GatewayError
// @Failure      503       {object}  core.GatewayError
// @Router       /admin/virtual-models [delete]
func (h *Handler) DeleteVirtualModel(c *echo.Context) error {
	if h.virtualModels == nil {
		return handleError(c, featureUnavailableError("virtual models feature is unavailable"))
	}

	var req deleteVirtualModelRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	source := strings.TrimSpace(req.Source)
	if source == "" {
		return handleError(c, core.NewInvalidRequestError("source is required", nil))
	}

	if err := h.virtualModels.Delete(c.Request().Context(), source); err != nil {
		if errors.Is(err, virtualmodels.ErrNotFound) {
			return handleError(c, core.NewNotFoundError("virtual model not found: "+source))
		}
		return handleError(c, virtualModelWriteError(err))
	}
	return c.NoContent(http.StatusNoContent)
}

// buildVirtualModelUpsert maps the request into a VirtualModel. Presence of
// target_model makes a redirect; otherwise it is an access policy. Enabled
// defaults to true, preserving the existing value when omitted.
func (h *Handler) buildVirtualModelUpsert(source string, req upsertVirtualModelRequest) (virtualmodels.VirtualModel, error) {
	enabled := true
	if existing, ok := h.virtualModels.Get(source); ok && existing != nil {
		enabled = existing.Enabled
	}
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	vm := virtualmodels.VirtualModel{
		Source:      source,
		UserPaths:   req.UserPaths,
		Description: strings.TrimSpace(req.Description),
		Enabled:     enabled,
	}

	if target := strings.TrimSpace(req.TargetModel); target != "" {
		selector, err := core.ParseModelSelector(target, "")
		if err != nil {
			return virtualmodels.VirtualModel{}, core.NewInvalidRequestError("invalid target_model: "+err.Error(), err)
		}
		vm.Targets = []virtualmodels.Target{{Provider: selector.Provider, Model: selector.Model}}
	}
	return vm, nil
}

// findVirtualModelView returns the admin view for a source after an upsert by
// matching it in the refreshed listing.
func (h *Handler) findVirtualModelView(source string) (virtualmodels.View, bool) {
	if stored, ok := h.virtualModels.Get(source); ok && stored != nil {
		source = stored.Source
	}
	for _, view := range h.virtualModels.ListViews() {
		if view.Source == source {
			return view, true
		}
	}
	return virtualmodels.View{}, false
}
