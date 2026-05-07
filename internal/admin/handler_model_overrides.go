package admin

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
	"gomodel/internal/modeloverrides"
)

type upsertModelOverrideRequest struct {
	UserPaths []string `json:"user_paths,omitempty"`
}

// ListModelOverrides handles GET /admin/api/v1/model-overrides.
//
// @Summary      List model access overrides
// @Description  Lists persisted model access overrides by global, provider-wide, model-wide, or exact selector.
// @Description  Selectors support global "/", provider-wide "provider/", model-wide "model", and exact "provider/model" scopes.
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Success      200  {array}   modeloverrides.View
// @Failure      401  {object}  core.GatewayError
// @Failure      503  {object}  core.GatewayError
// @Router       /admin/api/v1/model-overrides [get]
func (h *Handler) ListModelOverrides(c *echo.Context) error {
	if h.modelOverrides == nil {
		return handleError(c, featureUnavailableError("model overrides feature is unavailable"))
	}
	views := h.modelOverrides.ListViews()
	if views == nil {
		views = []modeloverrides.View{}
	}
	return c.JSON(http.StatusOK, views)
}

// UpsertModelOverride handles PUT /admin/api/v1/model-overrides/{selector}.
//
// @Summary      Create or update one model access override
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        selector  path      string                      true  "URL-encoded model selector such as /, openai/, gpt-4o-mini, or openai/gpt-4o-mini"
// @Param        override  body      upsertModelOverrideRequest  true  "Allowed user paths"
// @Success      200       {object}  modeloverrides.View
// @Failure      400       {object}  core.GatewayError
// @Failure      401       {object}  core.GatewayError
// @Failure      500       {object}  core.GatewayError
// @Failure      502       {object}  core.GatewayError
// @Failure      503       {object}  core.GatewayError
// @Router       /admin/api/v1/model-overrides/{selector} [put]
//
//nolint:dupl // structurally similar to UpsertModelPricingOverride but operates on different types and stores.
func (h *Handler) UpsertModelOverride(c *echo.Context) error {
	if h.modelOverrides == nil {
		return handleError(c, featureUnavailableError("model overrides feature is unavailable"))
	}

	selector, err := decodeModelOverridePathSelector(c.Param("selector"))
	if err != nil {
		return handleError(c, err)
	}

	var req upsertModelOverrideRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}

	if err := h.modelOverrides.Upsert(c.Request().Context(), modeloverrides.Override{
		Selector:  selector,
		UserPaths: req.UserPaths,
	}); err != nil {
		return handleError(c, modelOverrideWriteError(err))
	}

	override, ok := h.modelOverrides.Get(selector)
	if !ok || override == nil {
		slog.Error("model override service returned no override after upsert", "selector", selector)
		return handleError(c, core.NewProviderError("model_overrides", http.StatusInternalServerError, "model override update failed unexpectedly", nil))
	}
	return c.JSON(http.StatusOK, modeloverrides.View{
		Override:  *override,
		ScopeKind: override.ScopeKind(),
	})
}

// DeleteModelOverride handles DELETE /admin/api/v1/model-overrides/{selector}.
//
// @Summary      Delete one model access override
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        selector  path  string  true  "URL-encoded model selector"
// @Success      204       "No Content"
// @Failure      400       {object}  core.GatewayError
// @Failure      401       {object}  core.GatewayError
// @Failure      404       {object}  core.GatewayError
// @Failure      502       {object}  core.GatewayError
// @Failure      503       {object}  core.GatewayError
// @Router       /admin/api/v1/model-overrides/{selector} [delete]
func (h *Handler) DeleteModelOverride(c *echo.Context) error {
	var unavailableErr error
	var deleteFunc func(context.Context, string) error
	if h.modelOverrides == nil {
		unavailableErr = featureUnavailableError("model overrides feature is unavailable")
	} else {
		deleteFunc = h.modelOverrides.Delete
	}
	return deleteByName(
		c,
		unavailableErr,
		"selector",
		decodeModelOverridePathSelector,
		deleteFunc,
		modeloverrides.ErrNotFound,
		"model override not found: ",
		modelOverrideWriteError,
	)
}
