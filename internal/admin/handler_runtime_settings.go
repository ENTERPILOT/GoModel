package admin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/runtimesettings"
)

type runtimeSettingsResponse struct {
	Settings []ext.SettingDescriptor `json:"settings"`
}

type updateRuntimeSettingRequest struct {
	Value string `json:"value"`
}

// RuntimeSettings lists extension-defined settings for the Dashboard.
func (h *Handler) RuntimeSettings(c *echo.Context) error {
	settings := []ext.SettingDescriptor{}
	if h.runtimeSettings != nil {
		settings = h.runtimeSettings.List()
	}
	return c.JSON(http.StatusOK, runtimeSettingsResponse{Settings: settings})
}

// UpdateRuntimeSetting validates and persists one extension-defined setting.
func (h *Handler) UpdateRuntimeSetting(c *echo.Context) error {
	if h.runtimeSettings == nil {
		return handleError(c, featureUnavailableError("runtime settings are unavailable"))
	}
	var req updateRuntimeSettingRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	key := strings.TrimSpace(c.Param("key"))
	setting, err := h.runtimeSettings.Update(c.Request().Context(), key, req.Value)
	if err != nil {
		switch {
		case errors.Is(err, runtimesettings.ErrNotFound):
			return handleError(c, core.NewNotFoundError("runtime setting not found").WithCode("runtime_setting_not_found"))
		case errors.Is(err, runtimesettings.ErrLocked):
			return handleError(c, core.NewInvalidRequestError("runtime setting is managed by an environment variable", err))
		case errors.Is(err, runtimesettings.ErrInvalid):
			return handleError(c, core.NewInvalidRequestError(err.Error(), err))
		default:
			return handleError(c, featureUnavailableError("failed to save runtime setting: "+err.Error()))
		}
	}
	return c.JSON(http.StatusOK, setting)
}
