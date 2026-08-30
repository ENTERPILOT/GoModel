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
	"github.com/enterpilot/gomodel/internal/validation"
)

type createAuthKeyRequest struct {
	Name            string     `json:"name"`
	Description     string     `json:"description,omitempty"`
	UserPath        string     `json:"user_path,omitempty"`
	Labels          []string   `json:"labels,omitempty"`
	AllowedModels   []string   `json:"allowed_models,omitempty"`
	DashboardAccess bool       `json:"dashboard_access,omitempty"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
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

	allowedModels, err := h.normalizeAllowedModels(req.AllowedModels)
	if err != nil {
		return handleError(c, err)
	}

	issued, err := h.authKeys.Create(c.Request().Context(), authkeys.CreateInput{
		Name:            req.Name,
		Description:     req.Description,
		UserPath:        userPath,
		Labels:          req.Labels,
		AllowedModels:   allowedModels,
		DashboardAccess: req.DashboardAccess,
		ExpiresAt:       req.ExpiresAt,
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
	var req updateAuthKeyLabelsRequest
	return h.updateAuthKey(c, &req, func(ctx context.Context, id string) (*authkeys.View, error) {
		return h.authKeys.UpdateLabels(ctx, id, req.Labels)
	})
}

type updateAuthKeyAllowedModelsRequest struct {
	// Pointer so an omitted or null value is rejected instead of being
	// treated as an implicit clear of a restricted key.
	AllowedModels *[]string `json:"allowed_models"`
}

// UpdateAuthKeyAllowedModels handles PUT /admin/auth-keys/:id/allowed-models.
// The request selectors replace the key's model allowlist; an explicit empty
// list lifts the key-level restriction (user-path policies still apply).
func (h *Handler) UpdateAuthKeyAllowedModels(c *echo.Context) error {
	var req updateAuthKeyAllowedModelsRequest
	return h.updateAuthKey(c, &req, func(ctx context.Context, id string) (*authkeys.View, error) {
		if req.AllowedModels == nil {
			return nil, validation.NewError("allowed_models is required", nil)
		}
		allowedModels, err := h.normalizeAllowedModels(*req.AllowedModels)
		if err != nil {
			return nil, err
		}
		return h.authKeys.UpdateAllowedModels(ctx, id, allowedModels)
	})
}

// normalizeAllowedModels canonicalizes model selectors against the provider
// catalog when the users service is available, and syntactically otherwise.
func (h *Handler) normalizeAllowedModels(raw []string) ([]string, error) {
	if h.users == nil {
		return authkeys.NormalizeAllowedModels(raw), nil
	}
	allowed, err := h.users.NormalizeAllowedModels(raw)
	if err != nil {
		return nil, core.NewInvalidRequestError(err.Error(), err)
	}
	return allowed, nil
}

type updateAuthKeyDashboardAccessRequest struct {
	// Pointer so an omitted or null value is rejected instead of being
	// treated as an implicit revoke.
	DashboardAccess *bool `json:"dashboard_access"`
}

// UpdateAuthKeyDashboardAccess handles PUT /admin/auth-keys/:id/dashboard-access.
// It grants or revokes the key's access to the admin API and dashboard.
func (h *Handler) UpdateAuthKeyDashboardAccess(c *echo.Context) error {
	var req updateAuthKeyDashboardAccessRequest
	return h.updateAuthKey(c, &req, func(ctx context.Context, id string) (*authkeys.View, error) {
		if req.DashboardAccess == nil {
			return nil, validation.NewError("dashboard_access is required", nil)
		}
		return h.authKeys.UpdateDashboardAccess(ctx, id, *req.DashboardAccess)
	})
}

// updateAuthKey handles the shared shape of the PUT /admin/auth-keys/:id/*
// endpoints: bind the request into req, run the update, and render the
// updated view.
func (h *Handler) updateAuthKey(c *echo.Context, req any, update func(ctx context.Context, id string) (*authkeys.View, error)) error {
	if h.authKeys == nil {
		return handleError(c, featureUnavailableError("auth keys feature is unavailable"))
	}

	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return handleError(c, core.NewInvalidRequestError("auth key id is required", nil))
	}

	if err := c.Bind(req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}

	view, err := update(c.Request().Context(), id)
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
