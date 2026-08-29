package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/users"
)

// upsertUserRequest creates a user (empty id) or updates an existing one.
// The user_path is derived from the group chain plus the name and cannot be
// set directly.
type upsertUserRequest struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name" binding:"required"`
	Description string `json:"description,omitempty"`
	Group       string `json:"group,omitempty"`
}

type deleteUserRequest struct {
	ID string `json:"id" binding:"required"`
}

type upsertGroupRequest struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description,omitempty"`
	Parent      string `json:"parent,omitempty"`
}

type deleteGroupRequest struct {
	Name string `json:"name" binding:"required"`
}

// ListUsers handles GET /admin/users.
//
// @Summary      List registered users
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Success      200  {array}   users.User
// @Failure      401  {object}  core.GatewayError
// @Failure      503  {object}  core.GatewayError
// @Router       /admin/users [get]
func (h *Handler) ListUsers(c *echo.Context) error {
	if h.users == nil {
		return handleError(c, featureUnavailableError("users feature is unavailable"))
	}
	list := h.users.ListUsers()
	if list == nil {
		list = []users.User{}
	}
	return c.JSON(http.StatusOK, list)
}

// UpsertUser handles PUT /admin/users.
//
// @Summary      Create or update one user
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        user  body      upsertUserRequest  true  "User definition"
// @Success      200   {object}  users.User
// @Failure      400   {object}  core.GatewayError
// @Failure      401   {object}  core.GatewayError
// @Failure      404   {object}  core.GatewayError
// @Failure      502   {object}  core.GatewayError
// @Failure      503   {object}  core.GatewayError
// @Router       /admin/users [put]
func (h *Handler) UpsertUser(c *echo.Context) error {
	if h.users == nil {
		return handleError(c, featureUnavailableError("users feature is unavailable"))
	}
	var req upsertUserRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}

	user, err := h.users.UpsertUser(c.Request().Context(), users.UpsertUserInput{
		ID:          req.ID,
		Name:        req.Name,
		Description: req.Description,
		Group:       req.Group,
	})
	if err != nil {
		if errors.Is(err, users.ErrUserNotFound) {
			return handleError(c, core.NewNotFoundError("user not found: "+req.ID))
		}
		return handleError(c, usersWriteError(err))
	}
	return c.JSON(http.StatusOK, user)
}

// DeleteUser handles DELETE /admin/users.
//
// @Summary      Delete one user
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body  deleteUserRequest  true  "User id to remove"
// @Success      204      "No Content"
// @Failure      400      {object}  core.GatewayError
// @Failure      401      {object}  core.GatewayError
// @Failure      404      {object}  core.GatewayError
// @Failure      502      {object}  core.GatewayError
// @Failure      503      {object}  core.GatewayError
// @Router       /admin/users [delete]
func (h *Handler) DeleteUser(c *echo.Context) error {
	if h.users == nil {
		return handleError(c, featureUnavailableError("users feature is unavailable"))
	}
	var req deleteUserRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	return deleteUserRegistryItem(c, "id", req.ID, "user", h.users.DeleteUser, users.ErrUserNotFound)
}

// deleteUserRegistryItem is the shared delete tail for the user registry
// endpoints: validate the identifier, delete, and translate the store's
// not-found sentinel into a 404.
func deleteUserRegistryItem(
	c *echo.Context,
	field, value, noun string,
	del func(ctx context.Context, value string) error,
	notFound error,
) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return handleError(c, core.NewInvalidRequestError(field+" is required", nil))
	}
	if err := del(c.Request().Context(), value); err != nil {
		if errors.Is(err, notFound) {
			return handleError(c, core.NewNotFoundError(noun+" not found: "+value))
		}
		return handleError(c, usersWriteError(err))
	}
	return c.NoContent(http.StatusNoContent)
}

// ListUserGroups handles GET /admin/user-groups.
//
// @Summary      List user groups
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Success      200  {array}   users.Group
// @Failure      401  {object}  core.GatewayError
// @Failure      503  {object}  core.GatewayError
// @Router       /admin/user-groups [get]
func (h *Handler) ListUserGroups(c *echo.Context) error {
	if h.users == nil {
		return handleError(c, featureUnavailableError("users feature is unavailable"))
	}
	list := h.users.ListGroups()
	if list == nil {
		list = []users.Group{}
	}
	return c.JSON(http.StatusOK, list)
}

// UpsertUserGroup handles PUT /admin/user-groups.
//
// @Summary      Create or update one user group
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        group  body      upsertGroupRequest  true  "Group definition"
// @Success      200    {object}  users.Group
// @Failure      400    {object}  core.GatewayError
// @Failure      401    {object}  core.GatewayError
// @Failure      502    {object}  core.GatewayError
// @Failure      503    {object}  core.GatewayError
// @Router       /admin/user-groups [put]
func (h *Handler) UpsertUserGroup(c *echo.Context) error {
	if h.users == nil {
		return handleError(c, featureUnavailableError("users feature is unavailable"))
	}
	var req upsertGroupRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}

	group, err := h.users.UpsertGroup(c.Request().Context(), users.UpsertGroupInput{
		Name:        req.Name,
		Description: req.Description,
		Parent:      req.Parent,
	})
	if err != nil {
		return handleError(c, usersWriteError(err))
	}
	return c.JSON(http.StatusOK, group)
}

// DeleteUserGroup handles DELETE /admin/user-groups. A group that still has
// member users or child groups is refused with a validation error.
//
// @Summary      Delete one user group
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body  deleteGroupRequest  true  "Group name to remove"
// @Success      204      "No Content"
// @Failure      400      {object}  core.GatewayError
// @Failure      401      {object}  core.GatewayError
// @Failure      404      {object}  core.GatewayError
// @Failure      502      {object}  core.GatewayError
// @Failure      503      {object}  core.GatewayError
// @Router       /admin/user-groups [delete]
func (h *Handler) DeleteUserGroup(c *echo.Context) error {
	if h.users == nil {
		return handleError(c, featureUnavailableError("users feature is unavailable"))
	}
	var req deleteGroupRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	return deleteUserRegistryItem(c, "name", req.Name, "group", h.users.DeleteGroup, users.ErrGroupNotFound)
}
