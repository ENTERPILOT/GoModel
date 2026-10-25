package admin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/mcpgateway"
)

// MCPOAuthCallbackPath is the unauthenticated route authorization servers
// redirect to after an MCP server sign-in.
const MCPOAuthCallbackPath = mcpgateway.OAuthCallbackPath

// mcpOAuthBindingCookie binds a sign-in to the browser that started it, so a
// leaked authorization URL cannot be finished by someone else.
const mcpOAuthBindingCookie = "gomodel_mcp_oauth"

// MCPServerOAuth is the OAuth sign-in surface of *mcpgateway.Service. It is
// separate from MCPServerAdmin so gateways without it keep working.
type MCPServerOAuth interface {
	StartOAuth(ctx context.Context, name string, req mcpgateway.OAuthStartRequest) (mcpgateway.OAuthStart, error)
	CompleteOAuth(ctx context.Context, callback mcpgateway.OAuthCallback) (string, error)
	SignOutOAuth(ctx context.Context, name string) error
}

// startMCPOAuthRequest starts a sign-in. redirect_uri is the absolute URL of
// /admin/mcp-oauth/callback as the browser reaches the gateway. client_id and
// client_secret are only for authorization servers without dynamic client
// registration.
type startMCPOAuthRequest struct {
	RedirectURI  string `json:"redirect_uri"`
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
}

type startMCPOAuthResponse struct {
	AuthorizationURL string `json:"authorization_url"`
}

// mcpOAuthViewResponse is the sign-in state of one server. It never carries
// tokens or client secrets.
type mcpOAuthViewResponse struct {
	Status     string     `json:"status"`
	Issuer     string     `json:"issuer,omitempty"`
	Scopes     []string   `json:"scopes,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	SignedInAt *time.Time `json:"signed_in_at,omitempty"`
	LastError  string     `json:"last_error,omitempty"`
}

func (h *Handler) mcpOAuth() (MCPServerOAuth, bool) {
	if h.mcpServers == nil {
		return nil, false
	}
	oauth, ok := h.mcpServers.(MCPServerOAuth)
	return oauth, ok
}

// StartMCPServerOAuth handles POST /admin/mcp-servers/:name/oauth/start.
//
// @Summary      Start an OAuth sign-in to one MCP server
// @Description  Runs MCP authorization discovery (protected resource metadata, authorization server metadata, PKCE check, dynamic client registration when no client_id is given) and returns the authorization URL to open in the browser. The response sets a short-lived cookie the callback checks.
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        name     path      string                true  "MCP server slug"
// @Param        request  body      startMCPOAuthRequest  true  "Sign-in request"
// @Success      200      {object}  startMCPOAuthResponse
// @Failure      400      {object}  core.GatewayError
// @Failure      401      {object}  core.GatewayError
// @Failure      404      {object}  core.GatewayError
// @Failure      502      {object}  core.GatewayError
// @Failure      503      {object}  core.GatewayError
// @Router       /admin/mcp-servers/{name}/oauth/start [post]
func (h *Handler) StartMCPServerOAuth(c *echo.Context) error {
	oauth, ok := h.mcpOAuth()
	if !ok {
		return handleError(c, featureUnavailableError("mcp oauth sign-in is unavailable"))
	}
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		return handleError(c, core.NewInvalidRequestError("name is required", nil))
	}
	var req startMCPOAuthRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	redirectURI := strings.TrimSpace(req.RedirectURI)
	start, err := oauth.StartOAuth(c.Request().Context(), name, mcpgateway.OAuthStartRequest{
		RedirectURI:  redirectURI,
		ClientID:     req.ClientID,
		ClientSecret: req.ClientSecret,
	})
	if err != nil {
		return handleError(c, mcpOAuthError(name, err))
	}
	// Secure follows the redirect URI's scheme: a gateway reached over plain
	// HTTP on localhost needs the cookie too.
	cookie := &http.Cookie{ //nolint:gosec // Secure is set below from the redirect URI scheme.
		Name:     mcpOAuthBindingCookie,
		Value:    start.Binding,
		Path:     mcpgateway.OAuthCallbackPath,
		MaxAge:   600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	if parsed, err := url.Parse(redirectURI); err == nil {
		cookie.Path = parsed.Path
		cookie.Secure = parsed.Scheme == "https"
	}
	c.SetCookie(cookie)
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, startMCPOAuthResponse{AuthorizationURL: start.AuthorizationURL})
}

// SignOutMCPServerOAuth handles DELETE /admin/mcp-servers/:name/oauth.
//
// @Summary      Sign out of one MCP server
// @Description  Deletes the stored OAuth tokens and reconnects the server without them.
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        name  path  string  true  "MCP server slug"
// @Success      204   "No Content"
// @Failure      401   {object}  core.GatewayError
// @Failure      404   {object}  core.GatewayError
// @Failure      502   {object}  core.GatewayError
// @Failure      503   {object}  core.GatewayError
// @Router       /admin/mcp-servers/{name}/oauth [delete]
func (h *Handler) SignOutMCPServerOAuth(c *echo.Context) error {
	oauth, ok := h.mcpOAuth()
	if !ok {
		return handleError(c, featureUnavailableError("mcp oauth sign-in is unavailable"))
	}
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		return handleError(c, core.NewInvalidRequestError("name is required", nil))
	}
	if err := oauth.SignOutOAuth(c.Request().Context(), name); err != nil {
		return handleError(c, mcpOAuthError(name, err))
	}
	return c.NoContent(http.StatusNoContent)
}

// MCPOAuthCallback handles GET /admin/mcp-oauth/callback. It is reached by a
// browser redirect from the authorization server, without the dashboard's
// bearer token: the single-use state and the binding cookie authenticate it.
// It redirects back to the dashboard's MCP Servers page with the outcome;
// failure details are shown on the server's row, never in the URL.
func (h *Handler) MCPOAuthCallback(c *echo.Context) error {
	header := c.Response().Header()
	header.Set("Cache-Control", "no-store")
	header.Set("Referrer-Policy", "no-referrer")
	oauth, ok := h.mcpOAuth()
	if !ok {
		return handleError(c, featureUnavailableError("mcp oauth sign-in is unavailable"))
	}
	query := c.Request().URL.Query()
	callback := mcpgateway.OAuthCallback{
		State:            query.Get("state"),
		Code:             query.Get("code"),
		Issuer:           query.Get("iss"),
		Error:            query.Get("error"),
		ErrorDescription: query.Get("error_description"),
	}
	if cookie, err := c.Cookie(mcpOAuthBindingCookie); err == nil {
		callback.Binding = cookie.Value
	}
	c.SetCookie(&http.Cookie{ //nolint:gosec // Clears the cookie; it carries no value.
		Name:     mcpOAuthBindingCookie,
		Value:    "",
		Path:     c.Request().URL.Path,
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	server, err := oauth.CompleteOAuth(c.Request().Context(), callback)
	outcome := url.Values{}
	switch {
	case err == nil:
		outcome.Set("mcp_oauth", "signed_in")
	case errors.Is(err, mcpgateway.ErrOAuthFlowUnknown):
		outcome.Set("mcp_oauth", "expired")
	default:
		outcome.Set("mcp_oauth", "error")
	}
	if server != "" {
		outcome.Set("server", server)
	}
	// Relative to the callback, so it also works under a base path.
	return c.Redirect(http.StatusSeeOther, "../dashboard/mcp-servers?"+outcome.Encode())
}

// mcpOAuthError maps sign-in failures: unknown servers are 404, input the
// operator has to fix is 400, a missing client registration is 400 with
// code oauth_client_required, and upstream discovery failures are 502.
func mcpOAuthError(name string, err error) error {
	switch {
	case errors.Is(err, mcpgateway.ErrNotFound):
		return core.NewNotFoundError("mcp server not found: " + name)
	case errors.Is(err, mcpgateway.ErrOAuthUnavailable):
		return featureUnavailableError(err.Error())
	case errors.Is(err, mcpgateway.ErrOAuthClientRequired):
		return core.NewInvalidRequestError(err.Error(), err).WithParam("client_id").WithCode("oauth_client_required")
	case errors.Is(err, mcpgateway.ErrOAuthInvalidRequest):
		return core.NewInvalidRequestError(err.Error(), err)
	}
	return core.NewProviderError("mcp_servers", http.StatusBadGateway, err.Error(), err)
}

// mcpOAuthView converts the gateway's sign-in state for admin views.
func mcpOAuthView(view *mcpgateway.OAuthView) *mcpOAuthViewResponse {
	if view == nil {
		return nil
	}
	resp := &mcpOAuthViewResponse{
		Status:    view.Status,
		Issuer:    view.Issuer,
		Scopes:    view.Scopes,
		LastError: view.LastError,
	}
	if !view.ExpiresAt.IsZero() {
		value := view.ExpiresAt.UTC()
		resp.ExpiresAt = &value
	}
	if !view.SignedInAt.IsZero() {
		value := view.SignedInAt.UTC()
		resp.SignedInAt = &value
	}
	return resp
}
