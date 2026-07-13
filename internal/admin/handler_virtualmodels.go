package admin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
)

// upsertVirtualModelRequest is the unified admin upsert contract. Presence of
// target_model or targets makes the row a redirect; absence makes it an access
// policy. A single target_model is a plain alias; multiple targets are load
// balanced across by strategy ("round_robin" or "cost").
type upsertVirtualModelRequest struct {
	Source      string                      `json:"source"`
	OldSource   string                      `json:"old_source,omitempty"`
	TargetModel string                      `json:"target_model,omitempty"`
	Targets     []virtualModelTargetRequest `json:"targets,omitempty"`
	Strategy    string                      `json:"strategy,omitempty"`
	UserPaths   []string                    `json:"user_paths,omitempty"`
	Description string                      `json:"description,omitempty"`
	Enabled     *bool                       `json:"enabled,omitempty"`
}

// virtualModelTargetRequest is one load-balancing destination. Model may be a
// bare id (with provider set) or a "provider/model" selector. Weight biases the
// round_robin strategy and defaults to 1.
type virtualModelTargetRequest struct {
	Provider string  `json:"provider,omitempty"`
	Model    string  `json:"model"`
	Weight   float64 `json:"weight,omitempty"`
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

// UpsertVirtualModel handles PUT /admin/virtual-models. When old_source is set
// and differs from source, the row is renamed: stored under the new source and
// removed from the old one in a single validated operation.
//
// @Summary      Create, update, or rename one virtual model
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        virtual_model  body      upsertVirtualModelRequest  true  "Virtual model definition"
// @Success      200            {object}  virtualmodels.View
// @Failure      400            {object}  core.GatewayError
// @Failure      401            {object}  core.GatewayError
// @Failure      502            {object}  core.GatewayError
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
	oldSource := strings.TrimSpace(req.OldSource)
	if oldSource != "" && oldSource != source {
		err = h.virtualModels.Rename(c.Request().Context(), oldSource, vm)
	} else {
		err = h.virtualModels.Upsert(c.Request().Context(), vm)
	}
	if err != nil {
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
// @Failure      502       {object}  core.GatewayError
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
	vm := virtualmodels.VirtualModel{
		Source:      source,
		Strategy:    strings.TrimSpace(req.Strategy),
		UserPaths:   req.UserPaths,
		Description: strings.TrimSpace(req.Description),
		Enabled:     h.virtualModels.ResolveUpsertEnabled(source, req.OldSource, req.Enabled),
	}

	targets, err := buildVirtualModelTargets(req)
	if err != nil {
		return virtualmodels.VirtualModel{}, err
	}
	vm.Targets = targets
	return vm, nil
}

// buildVirtualModelTargets resolves the redirect targets from the request. The
// multi-target `targets` form takes precedence; a single `target_model` is the
// backward-compatible shorthand. An empty result makes the row an access policy.
func buildVirtualModelTargets(req upsertVirtualModelRequest) ([]virtualmodels.Target, error) {
	if len(req.Targets) > 0 {
		targets := make([]virtualmodels.Target, 0, len(req.Targets))
		for _, t := range req.Targets {
			model := strings.TrimSpace(t.Model)
			if model == "" {
				continue
			}
			selector, err := core.ParseModelSelector(model, strings.TrimSpace(t.Provider))
			if err != nil {
				return nil, core.NewInvalidRequestError("invalid target model "+model+": "+err.Error(), err)
			}
			targets = append(targets, virtualmodels.Target{
				Provider: selector.Provider,
				Model:    selector.Model,
				Weight:   t.Weight,
			})
		}
		// A targets list with only blank entries is a malformed redirect, not an
		// access policy — fail loudly rather than silently demoting it.
		if len(targets) == 0 {
			return nil, core.NewInvalidRequestError("targets must contain at least one model", nil)
		}
		return targets, nil
	}

	if target := strings.TrimSpace(req.TargetModel); target != "" {
		selector, err := core.ParseModelSelector(target, "")
		if err != nil {
			return nil, core.NewInvalidRequestError("invalid target_model: "+err.Error(), err)
		}
		return []virtualmodels.Target{{Provider: selector.Provider, Model: selector.Model}}, nil
	}
	return nil, nil
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
