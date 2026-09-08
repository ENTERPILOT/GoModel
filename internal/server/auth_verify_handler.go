package server

import (
	"crypto/subtle"
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
)

// authVerifyMethodNone is reported when the request carried no credential this
// gateway recognizes. Requests with an unusable credential are rejected by the
// authentication middleware long before the handler, so this is reached only
// where that middleware admits unauthenticated callers: a gateway with no
// authentication configured, or a route an extension excluded from it.
const authVerifyMethodNone = "none"

// authVerifyResponse is the answer of the credential check: whether the
// presented key authenticates against this gateway, and which mechanism
// accepted it. It never echoes the credential itself.
type authVerifyResponse struct {
	Valid bool `json:"valid"`
	// Method is "api_key" for a managed key stored in the database,
	// "master_key" for the bootstrap key, an extension-specific value for
	// identities supplied by an authentication extension, or "none" when the
	// request carried no credential this gateway recognizes, which is also
	// when Valid is false.
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
// @Description  Reports whether the presented credential authenticates against this gateway. Returns 401 when it does not. A gateway with no authentication configured has no credential to confirm and answers 200 with valid=false and method=none. Disabled unless AUTH_VERIFY_ENABLED is set.
// @Tags         auth
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  authVerifyResponse
// @Failure      401  {object}  core.OpenAIErrorEnvelope
// @Router       /v1/auth/verify [get]
func (h *Handler) AuthVerify(c *echo.Context) error {
	ctx := c.Request().Context()

	method := h.authVerifyMethod(c)
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

// authVerifyMethod names the mechanism that authenticated the request. Every
// answer rests on positive evidence rather than on the request having reached
// the handler: an endpoint that attests authentication must not infer it from
// a configuration where the middleware would normally have rejected the
// caller. A route excluded from authentication (an extension's skip path)
// therefore reports no credential instead of the configured master key.
//
// Managed keys and extension identities are read back from what the middleware
// already attached to the context, so the model routes pay nothing for this
// route; only the master key is re-compared here, on this request alone.
func (h *Handler) authVerifyMethod(c *echo.Context) string {
	ctx := c.Request().Context()
	if core.GetAuthKeyID(ctx) != "" {
		return auditlog.AuthMethodAPIKey
	}
	if authentication, ok := ext.AuthenticationFromContext(ctx); ok {
		if method := auditlog.NormalizeAuthMethod(authentication.Method); method != "" {
			return method
		}
		return auditlog.AuthMethodExtension
	}
	if h.masterKey != "" {
		if token, _ := requestAuthToken(c.Request()); token != "" &&
			subtle.ConstantTimeCompare([]byte(token), []byte(h.masterKey)) == 1 {
			return auditlog.AuthMethodMasterKey
		}
	}
	return authVerifyMethodNone
}
