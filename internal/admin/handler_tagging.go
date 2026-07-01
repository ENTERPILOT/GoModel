package admin

import (
	"net/http"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
	"gomodel/internal/tagging"
)

// taggingSettingsResponse is the tagging configuration exposed to the dashboard.
type taggingSettingsResponse struct {
	// Headers is the effective rule set: config/env-managed rules (read-only,
	// managed=true) followed by operator rules persisted in the admin store.
	Headers []tagging.Rule `json:"headers"`

	// Editable reports whether operator rules can be saved (false when no
	// settings storage is available).
	Editable bool `json:"editable"`
}

// updateTaggingSettingsRequest replaces the operator-managed rule set.
type updateTaggingSettingsRequest struct {
	Headers []tagging.Rule `json:"headers"`
}

// TaggingSettings handles GET /admin/tagging/settings.
// @Summary      Get header tagging rules
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  taggingSettingsResponse
// @Failure      401  {object}  core.GatewayError
// @Failure      503  {object}  core.GatewayError
// @Router       /admin/tagging/settings [get]
func (h *Handler) TaggingSettings(c *echo.Context) error {
	if h.tagging == nil {
		return handleError(c, featureUnavailableError("tagging feature is unavailable"))
	}
	return c.JSON(http.StatusOK, taggingSettingsResponse{
		Headers:  h.tagging.Rules(),
		Editable: h.tagging.Editable(),
	})
}

// UpdateTaggingSettings handles PUT /admin/tagging/settings. The request
// replaces the operator-managed rule set; config/env-declared rules are
// read-only and rejected.
// @Summary      Replace operator header tagging rules
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        settings  body      updateTaggingSettingsRequest  true  "Operator tagging rules"
// @Success      200       {object}  taggingSettingsResponse
// @Failure      400       {object}  core.GatewayError
// @Failure      401       {object}  core.GatewayError
// @Failure      503       {object}  core.GatewayError
// @Router       /admin/tagging/settings [put]
func (h *Handler) UpdateTaggingSettings(c *echo.Context) error {
	if h.tagging == nil {
		return handleError(c, featureUnavailableError("tagging feature is unavailable"))
	}
	var req updateTaggingSettingsRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	h.mutationMu.Lock()
	defer h.mutationMu.Unlock()
	merged, err := h.tagging.SaveRules(c.Request().Context(), req.Headers)
	if err != nil {
		if tagging.IsValidationError(err) {
			return handleError(c, core.NewInvalidRequestError(err.Error(), err))
		}
		return handleError(c, featureUnavailableError("failed to save tagging rules: "+err.Error()))
	}
	return c.JSON(http.StatusOK, taggingSettingsResponse{
		Headers:  merged,
		Editable: h.tagging.Editable(),
	})
}
