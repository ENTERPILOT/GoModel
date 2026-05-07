package admin

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
	"gomodel/internal/pricingoverrides"
)

type upsertModelPricingOverrideRequest struct {
	Pricing pricingoverrides.Pricing `json:"pricing" binding:"required"`
}

// ListModelPricingOverrides handles GET /admin/api/v1/model-pricing-overrides.
//
// @Summary      List model pricing overrides
// @Description  Lists persisted USD pricing overrides. Selectors support global "/", provider-wide "provider/", model-wide "model", and exact "provider/model" scopes.
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Success      200  {array}   pricingoverrides.View
// @Failure      401  {object}  core.GatewayError
// @Failure      503  {object}  core.GatewayError
// @Router       /admin/api/v1/model-pricing-overrides [get]
func (h *Handler) ListModelPricingOverrides(c *echo.Context) error {
	if h.pricingOverrides == nil {
		return handleError(c, featureUnavailableError("model pricing overrides feature is unavailable"))
	}
	views := h.pricingOverrides.ListViews()
	if views == nil {
		views = []pricingoverrides.View{}
	}
	return c.JSON(http.StatusOK, views)
}

// UpsertModelPricingOverride handles PUT /admin/api/v1/model-pricing-overrides/{selector}.
//
// @Summary      Create or update one model pricing override
// @Description  Stores USD-only pricing for one selector. More precise selectors override broader selectors at runtime.
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        selector  path      string                              true  "URL-encoded pricing selector such as /, openai/, gpt-4o-mini, or openai/gpt-4o-mini"
// @Param        override  body      upsertModelPricingOverrideRequest  true  "Pricing override"
// @Success      200       {object}  pricingoverrides.View
// @Failure      400       {object}  core.GatewayError
// @Failure      401       {object}  core.GatewayError
// @Failure      500       {object}  core.GatewayError
// @Failure      503       {object}  core.GatewayError
// @Router       /admin/api/v1/model-pricing-overrides/{selector} [put]
//
//nolint:dupl // structurally similar to UpsertModelOverride but operates on different types and stores.
func (h *Handler) UpsertModelPricingOverride(c *echo.Context) error {
	if h.pricingOverrides == nil {
		return handleError(c, featureUnavailableError("model pricing overrides feature is unavailable"))
	}

	selector, err := decodeModelPricingOverridePathSelector(c.Param("selector"))
	if err != nil {
		return handleError(c, err)
	}

	var req upsertModelPricingOverrideRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}

	if err := h.pricingOverrides.Upsert(c.Request().Context(), pricingoverrides.Override{
		Selector: selector,
		Pricing:  req.Pricing,
	}); err != nil {
		return handleError(c, pricingOverrideWriteError(err))
	}

	view, ok := h.pricingOverrides.GetView(selector)
	if !ok || view == nil {
		slog.Error("model pricing override service returned no override after upsert", "selector", selector)
		return handleError(c, core.NewProviderError("model_pricing_overrides", http.StatusInternalServerError, "model pricing override update failed unexpectedly", nil))
	}
	return c.JSON(http.StatusOK, view)
}

// DeleteModelPricingOverride handles DELETE /admin/api/v1/model-pricing-overrides/{selector}.
//
// @Summary      Delete one model pricing override
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        selector  path  string  true  "URL-encoded pricing selector"
// @Success      204       "No Content"
// @Failure      400       {object}  core.GatewayError
// @Failure      401       {object}  core.GatewayError
// @Failure      404       {object}  core.GatewayError
// @Failure      503       {object}  core.GatewayError
// @Router       /admin/api/v1/model-pricing-overrides/{selector} [delete]
func (h *Handler) DeleteModelPricingOverride(c *echo.Context) error {
	var unavailableErr error
	var deleteFunc func(context.Context, string) error
	if h.pricingOverrides == nil {
		unavailableErr = featureUnavailableError("model pricing overrides feature is unavailable")
	} else {
		deleteFunc = h.pricingOverrides.Delete
	}
	return deleteByName(
		c,
		unavailableErr,
		"selector",
		decodeModelPricingOverridePathSelector,
		deleteFunc,
		pricingoverrides.ErrNotFound,
		"model pricing override not found: ",
		pricingOverrideWriteError,
	)
}
