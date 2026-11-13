package admin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/oauthlogin"
)

// startOAuthLoginResponse is what the operator needs to approve a device
// login. It never carries the device code or any token: the device code
// redeems the grant, so it stays inside the gateway.
type startOAuthLoginResponse struct {
	// VerificationURL is where the user approves the login.
	VerificationURL string `json:"verification_url"`
	// UserCode is the code the user enters at the verification URL.
	UserCode string `json:"user_code"`
	// ExpiresIn is how many seconds the code stays valid.
	ExpiresIn int `json:"expires_in"`
	// Interval is how many seconds the gateway waits between polls.
	Interval int `json:"interval"`
}

// oauthLoginStatusResponse reports a device login. While it is pending, the
// verification URL and code are repeated so a reloaded page can still show
// them. Nothing in it authenticates a request.
type oauthLoginStatusResponse struct {
	// Status is "pending" until the user approves, then "signed_in".
	Status string `json:"status"`
	// VerificationURL and UserCode are set only while the login is pending.
	VerificationURL string `json:"verification_url,omitempty"`
	UserCode        string `json:"user_code,omitempty"`
	ExpiresIn       int    `json:"expires_in,omitempty"`
}

// StartOAuthLogin handles POST /admin/provider-credentials/:name/oauth/start.
//
// @Summary      Start a device login for an oauthlogin provider
// @Description  Begins an OAuth 2.0 device authorization grant against the provider's configured authorization server and returns the verification URL and user code. The gateway polls the token endpoint until the user approves, then stores the tokens.
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        name  path  string  true  "Provider name"
// @Success      200  {object}  startOAuthLoginResponse
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Failure      404  {object}  core.GatewayError
// @Failure      502  {object}  core.GatewayError
// @Failure      503  {object}  core.GatewayError
// @Router       /admin/provider-credentials/{name}/oauth/start [post]
func (h *Handler) StartOAuthLogin(c *echo.Context) error {
	provider, err := h.oauthLoginProvider(c, c.Param("name"))
	if err != nil {
		return handleError(c, err)
	}
	auth, err := provider.StartLogin(c.Request().Context())
	if err != nil {
		return handleError(c, oauthLoginError(err))
	}
	return c.JSON(http.StatusOK, startOAuthLoginResponse{
		VerificationURL: auth.VerificationURL,
		UserCode:        auth.UserCode,
		ExpiresIn:       auth.ExpiresIn,
		Interval:        auth.Interval,
	})
}

// GetOAuthLogin handles GET /admin/provider-credentials/:name/oauth.
//
// @Summary      Read the state of an oauthlogin provider's device login
// @Description  Reports whether the device login started for the provider is still waiting for the user to approve it or has completed. A rejected or expired grant reports the reason.
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        name  path  string  true  "Provider name"
// @Success      200  {object}  oauthLoginStatusResponse
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Failure      404  {object}  core.GatewayError
// @Router       /admin/provider-credentials/{name}/oauth [get]
func (h *Handler) GetOAuthLogin(c *echo.Context) error {
	provider, err := h.oauthLoginProvider(c, c.Param("name"))
	if err != nil {
		return handleError(c, err)
	}
	result := provider.Login()
	if result.Err != nil {
		return handleError(c, oauthLoginError(result.Err))
	}
	if result.Pending {
		return c.JSON(http.StatusOK, oauthLoginStatusResponse{
			Status:          "pending",
			VerificationURL: result.Authorization.VerificationURL,
			UserCode:        result.Authorization.UserCode,
			ExpiresIn:       result.Authorization.ExpiresIn,
		})
	}
	return c.JSON(http.StatusOK, oauthLoginStatusResponse{Status: "signed_in"})
}

// oauthLoginProvider resolves the named provider to its running oauthlogin
// instance. The credential must be of the oauthlogin type and the instance
// must be registered, so a login cannot be started against a provider that is
// not serving traffic.
func (h *Handler) oauthLoginProvider(c *echo.Context, name string) (*oauthlogin.Provider, error) {
	if h.providerCredentials == nil || h.registry == nil {
		return nil, featureUnavailableError("provider credentials feature is unavailable")
	}
	name = strings.TrimSpace(name)
	cred, err := h.providerCredentials.Get(c.Request().Context(), name)
	if err != nil {
		if errors.Is(err, providers.ErrCredentialNotFound) {
			return nil, core.NewNotFoundError("provider credential " + name + " not found")
		}
		return nil, providerCredentialWriteError(err)
	}
	if cred.Type != "oauthlogin" {
		return nil, core.NewInvalidRequestError("provider "+name+" is not an oauthlogin provider", nil).WithParam("type")
	}
	instance := h.registry.ProviderByName(name)
	if instance == nil {
		return nil, core.NewNotFoundError("provider " + name + " is not registered")
	}
	provider, ok := instance.(*oauthlogin.Provider)
	if !ok {
		return nil, core.NewInvalidRequestError("provider "+name+" is not an oauthlogin provider", nil).WithParam("type")
	}
	return provider, nil
}

// oauthLoginError renders a device-login failure. A grant the authorization
// server rejected is the operator's to act on, so it is a 400; a failure to
// reach the authorization server is a 502. The text is the error's own: the
// grant client never puts token material in one.
func oauthLoginError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[*core.GatewayError](err); ok {
		return err
	}
	return core.NewProviderError("oauthlogin", http.StatusBadGateway, err.Error(), err)
}
