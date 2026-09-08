package server

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
)

// authVerifyMethodNone is reported when the request reached the handler
// without any credential being checked, which only happens on a gateway that
// has no authentication configured at all.
const authVerifyMethodNone = "none"

// authVerifyResponse is the answer of the credential check: whether the
// presented key authenticates against this gateway, and which mechanism
// accepted it. It never echoes the credential itself.
type authVerifyResponse struct {
	Valid bool `json:"valid"`
	// Method is "api_key" for a managed key stored in the database,
	// "master_key" for the bootstrap key, an extension-specific value for
	// identities supplied by an authentication extension, or "none" when the
	// gateway checked no credential.
	Method string `json:"method"`
	// KeyID identifies the managed auth key that authenticated the request.
	// Empty for every other method.
	KeyID string `json:"key_id,omitempty"`
	// UserPath is the subtree the credential is bound to, empty when the
	// credential is global.
	UserPath string `json:"user_path,omitempty"`
}

// AuthVerify handles GET /v1/auth/verify.
//
// The route exists so a service in front of the gateway can ask whether an API
// key is usable without holding a copy of the key list. It is disabled by
// default and enabled with server.auth_verify_enabled (AUTH_VERIFY_ENABLED);
// it lives outside /admin so it stays reachable when the admin API is off.
//
// The endpoint adds no credential check of its own: the request passes the
// same authentication middleware as every model route, so an invalid, expired,
// or unknown key is rejected there with 401 and never reaches this handler. A
// 200 therefore means the credential authenticates, and the body reports which
// mechanism accepted it — "api_key" is the managed-key case, the one backed by
// a database row. On a gateway with no authentication configured every request
// is accepted, so the answer is valid=false with method "none": there is no
// key to confirm.
//
// @Summary      Verify an API key
// @Description  Reports whether the presented credential authenticates against this gateway. Returns 401 when it does not. Disabled unless AUTH_VERIFY_ENABLED is set.
// @Tags         auth
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  authVerifyResponse
// @Failure      401  {object}  core.OpenAIErrorEnvelope
// @Router       /v1/auth/verify [get]
func (h *Handler) AuthVerify(c *echo.Context) error {
	ctx := c.Request().Context()

	method := h.authVerifyMethod(ctx)
	response := authVerifyResponse{
		Valid:    method != authVerifyMethodNone,
		Method:   method,
		KeyID:    core.GetAuthKeyID(ctx),
		UserPath: core.AccessScopeFromContext(ctx).UserPath,
	}

	// The answer describes the caller's credential, so no proxy in between
	// may serve it to anyone else.
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, response)
}

// authVerifyMethod names the mechanism that let the request through. It is
// read back from what the authentication middleware already attached to the
// context rather than published as another context value, so the credential
// check costs the model routes nothing.
func (h *Handler) authVerifyMethod(ctx context.Context) string {
	if core.GetAuthKeyID(ctx) != "" {
		return auditlog.AuthMethodAPIKey
	}
	if authentication, ok := ext.AuthenticationFromContext(ctx); ok {
		if method := auditlog.NormalizeAuthMethod(authentication.Method); method != "" {
			return method
		}
		return auditlog.AuthMethodExtension
	}
	// Nothing else can have satisfied the middleware: without a master key,
	// a request carrying no managed or extension identity never reaches a
	// handler unless the gateway authenticates nobody at all.
	if h.masterKeyConfigured {
		return auditlog.AuthMethodMasterKey
	}
	return authVerifyMethodNone
}
