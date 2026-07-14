package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/authkeys"
	"github.com/enterpilot/gomodel/internal/core"
)

type createAuthKeyRequest struct {
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	UserPath    string     `json:"user_path,omitempty"`
	Labels      []string   `json:"labels,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

func (h *Handler) ListAuthKeys(c *echo.Context) error {
	if h.authKeys == nil {
		return handleError(c, featureUnavailableError("auth keys feature is unavailable"))
	}
	views := h.authKeys.ListViews()
	if views == nil {
		views = []authkeys.View{}
	}
	return c.JSON(http.StatusOK, views)
}

// CreateAuthKey handles POST /admin/auth-keys
func (h *Handler) CreateAuthKey(c *echo.Context) error {
	if h.authKeys == nil {
		return handleError(c, featureUnavailableError("auth keys feature is unavailable"))
	}

	var req createAuthKeyRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}

	userPath, err := normalizeUserPathQueryParam("user_path", req.UserPath)
	if err != nil {
		return handleError(c, err)
	}

	issued, err := h.authKeys.Create(c.Request().Context(), authkeys.CreateInput{
		Name:        req.Name,
		Description: req.Description,
		UserPath:    userPath,
		Labels:      req.Labels,
		ExpiresAt:   req.ExpiresAt,
	})
	if err != nil {
		return handleError(c, authKeyWriteError(err))
	}
	if issued == nil {
		requestID := strings.TrimSpace(core.GetRequestID(c.Request().Context()))
		slog.Error("auth key service returned nil issued key", "request_id", requestID, "path", c.Request().URL.Path)
		return c.JSON(http.StatusInternalServerError, (&core.GatewayError{
			Type:       core.ErrorType("internal_error"),
			Message:    "auth key creation failed unexpectedly",
			StatusCode: http.StatusInternalServerError,
		}).WithCode("auth_key_issue_failed").ToJSON())
	}
	return c.JSON(http.StatusCreated, issued)
}

type updateAuthKeyLabelsRequest struct {
	Labels []string `json:"labels"`
}

// UpdateAuthKeyLabels handles PUT /admin/auth-keys/:id/labels. The request
// labels replace the key's labels; an empty list clears them.
func (h *Handler) UpdateAuthKeyLabels(c *echo.Context) error {
	if h.authKeys == nil {
		return handleError(c, featureUnavailableError("auth keys feature is unavailable"))
	}

	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return handleError(c, core.NewInvalidRequestError("auth key id is required", nil))
	}

	var req updateAuthKeyLabelsRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}

	view, err := h.authKeys.UpdateLabels(c.Request().Context(), id, req.Labels)
	if err != nil {
		if errors.Is(err, authkeys.ErrNotFound) {
			return handleError(c, core.NewNotFoundError("auth key not found: "+id))
		}
		return handleError(c, authKeyWriteError(err))
	}
	return c.JSON(http.StatusOK, view)
}

// DeactivateAuthKey handles POST /admin/auth-keys/:id/deactivate
func (h *Handler) DeactivateAuthKey(c *echo.Context) error {
	var unavailableErr error
	var deactivate func(context.Context, string) error
	if h.authKeys == nil {
		unavailableErr = featureUnavailableError("auth keys feature is unavailable")
	} else {
		deactivate = h.authKeys.Deactivate
	}
	return deactivateByID(c, unavailableErr, "auth key", authkeys.ErrNotFound, "auth key not found: ", deactivate, authKeyWriteError)
}
