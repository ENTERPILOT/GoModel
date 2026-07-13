package admin

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/pricingoverrides"
)

type upsertModelPricingOverrideRequest struct {
	Selector string                   `json:"selector"`
	Pricing  pricingoverrides.Pricing `json:"pricing"`
}

type deleteModelPricingOverrideRequest struct {
	Selector string `json:"selector"`
}

// ListModelPricingOverrides handles GET /admin/model-pricing-overrides.
//
// @Summary      List model pricing overrides
// @Description  Lists persisted USD pricing overrides. Selectors support global "/", provider-wide "provider/", model-wide "model", and exact "provider/model" scopes.
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Success      200  {array}   pricingoverrides.View
// @Failure      401  {object}  core.GatewayError
// @Failure      503  {object}  core.GatewayError
// @Router       /admin/model-pricing-overrides [get]
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

// UpsertModelPricingOverride handles PUT /admin/model-pricing-overrides.
//
// @Summary      Create or update one model pricing override
// @Description  Stores USD-only pricing for one selector. More precise selectors override broader selectors at runtime.
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        override  body      upsertModelPricingOverrideRequest  true  "Pricing selector and override"
// @Success      200       {object}  pricingoverrides.View
// @Failure      400       {object}  core.GatewayError
// @Failure      401       {object}  core.GatewayError
// @Failure      500       {object}  core.GatewayError
// @Failure      502       {object}  core.GatewayError
// @Failure      503       {object}  core.GatewayError
// @Router       /admin/model-pricing-overrides [put]
//
//nolint:dupl // structurally similar to UpsertModelOverride but operates on different types and stores.
func (h *Handler) UpsertModelPricingOverride(c *echo.Context) error {
	if h.pricingOverrides == nil {
		return handleError(c, featureUnavailableError("model pricing overrides feature is unavailable"))
	}

	var req upsertModelPricingOverrideRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	selector, err := normalizeModelPricingOverrideSelector(req.Selector)
	if err != nil {
		return handleError(c, err)
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

// DeleteModelPricingOverride handles DELETE /admin/model-pricing-overrides.
//
// @Summary      Delete one model pricing override
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body  deleteModelPricingOverrideRequest  true  "Pricing selector to remove"
// @Success      204       "No Content"
// @Failure      400       {object}  core.GatewayError
// @Failure      401       {object}  core.GatewayError
// @Failure      404       {object}  core.GatewayError
// @Failure      502       {object}  core.GatewayError
// @Failure      503       {object}  core.GatewayError
// @Router       /admin/model-pricing-overrides [delete]
//
//nolint:dupl // structurally similar to DeleteModelOverride but operates on different types and stores.
func (h *Handler) DeleteModelPricingOverride(c *echo.Context) error {
	if h.pricingOverrides == nil {
		return handleError(c, featureUnavailableError("model pricing overrides feature is unavailable"))
	}

	var req deleteModelPricingOverrideRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	selector, err := normalizeModelPricingOverrideSelector(req.Selector)
	if err != nil {
		return handleError(c, err)
	}

	if err := h.pricingOverrides.Delete(c.Request().Context(), selector); err != nil {
		if errors.Is(err, pricingoverrides.ErrNotFound) {
			return handleError(c, core.NewNotFoundError("model pricing override not found: "+selector))
		}
		return handleError(c, pricingOverrideWriteError(err))
	}
	return c.NoContent(http.StatusNoContent)
}
