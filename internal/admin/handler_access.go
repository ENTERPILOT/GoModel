package admin

import (
	"net/http"

	"github.com/labstack/echo/v5"
)

const (
	accessScopeGlobal   = "global"
	accessScopeUserPath = "user_path"
)

// accessResponse describes how far the caller's admin credential reaches.
type accessResponse struct {
	// Scope is "global" for the master key and unscoped credentials, or
	// "user_path" for a credential confined to one user-path subtree.
	Scope string `json:"scope"`
	// UserPath is the subtree root of a user_path-scoped credential.
	UserPath string `json:"user_path,omitempty"`
}

// Access handles GET /admin/access.
//
// @Summary      Describe the caller's admin scope
// @Description  Reports whether the credential administers the whole gateway
// @Description  or only one user-path subtree, so clients can adapt what
// @Description  they offer before hitting scoped endpoints.
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  accessResponse
// @Failure      401  {object}  core.GatewayError
// @Router       /admin/access [get]
func (h *Handler) Access(c *echo.Context) error {
	scope := requestScope(c)
	if scope.Global() {
		return c.JSON(http.StatusOK, accessResponse{Scope: accessScopeGlobal})
	}
	return c.JSON(http.StatusOK, accessResponse{Scope: accessScopeUserPath, UserPath: scope.UserPath})
}
