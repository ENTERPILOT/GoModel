package dashboardlogin

import (
	"io"
	"math"
	"net/http"
	"strconv"
	"time"

	json "github.com/goccy/go-json"
	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/core"
)

// SessionPath is the route the dashboard signs in and out through. It is
// served without a credential: signing in is how a browser gets one.
const SessionPath = "/admin/session"

// maxCredentialsBytes bounds the sign-in body; a username and a password
// never come near it.
const maxCredentialsBytes = 8 * 1024

// Handler serves the session routes.
type Handler struct {
	service    *Service
	cookiePath string
}

// NewHandler creates the session routes for an app mounted under basePath.
func NewHandler(service *Service, basePath string) *Handler {
	return &Handler{service: service, cookiePath: config.NormalizeBasePath(basePath)}
}

// Authenticator returns the request authenticator that accepts the sessions
// this handler starts.
func (h *Handler) Authenticator() ext.RequestAuthenticator {
	return h.service
}

// RegisterRoutes mounts the session routes.
func (h *Handler) RegisterRoutes(e *echo.Echo) {
	e.GET(SessionPath, h.Session)
	e.POST(SessionPath, h.SignIn)
	e.DELETE(SessionPath, h.SignOut)
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// sessionResponse describes the caller's dashboard session.
type sessionResponse struct {
	Authenticated bool `json:"authenticated"`
	// Username and ExpiresAt are present only for an active session.
	Username  string     `json:"username,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// Session handles GET /admin/session.
//
// @Summary      Describe the dashboard session
// @Description  Reports whether the request carries an active dashboard session cookie. Registered only when the dashboard login is configured.
// @Tags         admin
// @Produce      json
// @Success      200  {object}  sessionResponse
// @Router       /admin/session [get]
func (h *Handler) Session(c *echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	expires, ok := h.service.session(c.Request())
	if !ok {
		return c.JSON(http.StatusOK, sessionResponse{})
	}
	return c.JSON(http.StatusOK, h.active(expires))
}

// SignIn handles POST /admin/session.
//
// @Summary      Sign in to the dashboard
// @Description  Exchanges the configured dashboard username and password for a session cookie that authenticates the dashboard and the admin API. Registered only when the dashboard login is configured.
// @Tags         admin
// @Accept       json
// @Produce      json
// @Param        credentials  body      credentials  true  "Dashboard username and password"
// @Success      200  {object}  sessionResponse
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Failure      403  {object}  core.GatewayError
// @Failure      429  {object}  core.GatewayError
// @Router       /admin/session [post]
func (h *Handler) SignIn(c *echo.Context) error {
	request := c.Request()
	c.Response().Header().Set("Cache-Control", "no-store")
	if !sameOrigin(request) {
		return writeError(c, core.NewPermissionError("sign-in must come from the dashboard").WithCode("cross_origin_sign_in"))
	}

	now := h.service.now()
	clientIP := c.RealIP()
	if wait := h.service.attempts.admit(clientIP, now); wait > 0 {
		// Not recorded: the failures that caused the lockout already are, and
		// a locked-out client must not be able to fill the request log.
		c.Response().Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		return writeError(c, core.NewRateLimitError("", "too many failed sign-in attempts; try again later").WithCode("too_many_sign_in_attempts"))
	}

	var input credentials
	body, err := io.ReadAll(io.LimitReader(request.Body, maxCredentialsBytes+1))
	if err != nil || len(body) > maxCredentialsBytes || json.Unmarshal(body, &input) != nil {
		return writeError(c, core.NewInvalidRequestError("expected a JSON body with username and password", nil))
	}
	if !h.service.credentialsMatch(input.Username, input.Password) {
		h.record(c, "login", "failure", "invalid_credentials")
		return writeError(c, core.NewAuthenticationError("", "invalid username or password").WithCode("invalid_sign_in"))
	}

	h.service.attempts.reset(clientIP)
	token, expires := h.service.issue()
	c.SetCookie(h.cookie(c, token, int(SessionTTL.Seconds())))
	h.record(c, "login", "success", "")
	return c.JSON(http.StatusOK, h.active(expires))
}

// SignOut handles DELETE /admin/session.
//
// @Summary      Sign out of the dashboard
// @Description  Clears the dashboard session cookie. Registered only when the dashboard login is configured.
// @Tags         admin
// @Success      204
// @Router       /admin/session [delete]
func (h *Handler) SignOut(c *echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	if _, ok := h.service.session(c.Request()); ok {
		h.record(c, "logout", "success", "")
	}
	c.SetCookie(h.cookie(c, "", -1))
	return c.NoContent(http.StatusNoContent)
}

func (h *Handler) active(expires time.Time) sessionResponse {
	expires = expires.UTC()
	return sessionResponse{Authenticated: true, Username: h.service.username, ExpiresAt: &expires}
}

// cookie builds the session cookie. HttpOnly keeps it from page script, and
// SameSite=Strict keeps the browser from attaching it to a request another
// site started. It is marked Secure whenever the sign-in itself came over
// HTTPS, directly or through a trusted proxy: a gateway reached over plain
// HTTP on a private network needs the cookie too.
func (h *Handler) cookie(c *echo.Context, value string, maxAge int) *http.Cookie {
	return &http.Cookie{ //nolint:gosec // Secure follows the request scheme; see above.
		Name:     CookieName,
		Value:    value,
		Path:     h.cookiePath,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   c.Scheme() == "https",
		SameSite: http.SameSiteStrictMode,
	}
}

// record writes a sign-in or sign-out to the request log. It never carries
// the submitted username: a password typed into the wrong field would land in
// the log with it.
func (h *Handler) record(c *echo.Context, eventType, outcome, reason string) {
	recorder := h.service.recorder.Load()
	if recorder == nil {
		return
	}
	request := c.Request()
	event := ext.AuthenticationEvent{
		Timestamp:  h.service.now(),
		Type:       eventType,
		Outcome:    outcome,
		Method:     AuthMethod,
		RequestID:  request.Header.Get(core.RequestIDHeader),
		ClientIP:   c.RealIP(),
		HTTPMethod: request.Method,
		Path:       request.URL.Path,
		UserAgent:  request.UserAgent(),
		Reason:     reason,
	}
	if outcome == "success" {
		event.PrincipalID = h.service.username
	}
	(*recorder).RecordAuthenticationEvent(event)
}

func writeError(c *echo.Context, err *core.GatewayError) error {
	return c.JSON(err.HTTPStatusCode(), err.ToJSON())
}
