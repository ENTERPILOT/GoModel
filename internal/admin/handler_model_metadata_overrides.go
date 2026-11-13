package admin

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/metadataoverrides"
)

type upsertModelMetadataOverrideRequest struct {
	Selector string                     `json:"selector"`
	Metadata metadataoverrides.Metadata `json:"metadata"`
}

type deleteModelMetadataOverrideRequest struct {
	Selector string `json:"selector"`
}

func metadataOverridesUnavailable() error {
	return featureUnavailableError("model metadata overrides feature is unavailable")
}

func metadataOverrideWriteError(err error) error {
	if metadataoverrides.IsValidationError(err) {
		return core.NewInvalidRequestError(err.Error(), err)
	}
	return core.NewProviderError("model_metadata_overrides", http.StatusBadGateway, err.Error(), err)
}

// ListModelMetadataOverrides handles GET /admin/model-metadata-overrides.
//
// @Summary      List model metadata overrides
// @Description  Lists dashboard-managed per-model metadata overrides (categories, input capabilities, context window, max output tokens). Selectors are exact "provider/model".
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Success      200  {array}   metadataoverrides.Override
// @Failure      401  {object}  core.GatewayError
// @Failure      503  {object}  core.GatewayError
// @Router       /admin/model-metadata-overrides [get]
func (h *Handler) ListModelMetadataOverrides(c *echo.Context) error {
	if h.metadataOverrides == nil {
		return handleError(c, metadataOverridesUnavailable())
	}
	return c.JSON(http.StatusOK, h.metadataOverrides.List())
}

// UpsertModelMetadataOverride handles PUT /admin/model-metadata-overrides.
//
// @Summary      Create or replace one model metadata override
// @Description  Replaces the override for one "provider/model" selector. Set fields win over config.yaml metadata, provider discovery and the model catalog; unset fields inherit. Applies without a restart.
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        override  body      upsertModelMetadataOverrideRequest  true  "Selector and metadata override"
// @Success      200       {object}  metadataoverrides.Override
// @Failure      400       {object}  core.GatewayError
// @Failure      401       {object}  core.GatewayError
// @Failure      502       {object}  core.GatewayError
// @Failure      503       {object}  core.GatewayError
// @Router       /admin/model-metadata-overrides [put]
func (h *Handler) UpsertModelMetadataOverride(c *echo.Context) error {
	if h.metadataOverrides == nil {
		return handleError(c, metadataOverridesUnavailable())
	}
	var req upsertModelMetadataOverrideRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	selector, err := normalizeModelOverrideSelector("model metadata override", req.Selector)
	if err != nil {
		return handleError(c, err)
	}
	saved, err := h.metadataOverrides.Upsert(c.Request().Context(), metadataoverrides.Override{
		Selector: selector,
		Metadata: req.Metadata,
	})
	if err != nil {
		return handleError(c, metadataOverrideWriteError(err))
	}
	return c.JSON(http.StatusOK, saved)
}

// DeleteModelMetadataOverride handles DELETE /admin/model-metadata-overrides.
//
// @Summary      Delete one model metadata override
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body  deleteModelMetadataOverrideRequest  true  "Selector to remove"
// @Success      204       "No Content"
// @Failure      400       {object}  core.GatewayError
// @Failure      401       {object}  core.GatewayError
// @Failure      404       {object}  core.GatewayError
// @Failure      502       {object}  core.GatewayError
// @Failure      503       {object}  core.GatewayError
// @Router       /admin/model-metadata-overrides [delete]
func (h *Handler) DeleteModelMetadataOverride(c *echo.Context) error {
	if h.metadataOverrides == nil {
		return handleError(c, metadataOverridesUnavailable())
	}
	var req deleteModelMetadataOverrideRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	selector, err := normalizeModelOverrideSelector("model metadata override", req.Selector)
	if err != nil {
		return handleError(c, err)
	}
	if err := h.metadataOverrides.Delete(c.Request().Context(), selector); err != nil {
		if errors.Is(err, metadataoverrides.ErrNotFound) {
			return handleError(c, core.NewNotFoundError("model metadata override not found: "+selector))
		}
		return handleError(c, metadataOverrideWriteError(err))
	}
	return c.NoContent(http.StatusNoContent)
}
