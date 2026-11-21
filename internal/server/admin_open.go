package server

import (
	"net"
	"net/http"
	"net/netip"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/requestlog"
)

// forwardingHeaders name the headers a reverse proxy adds for the client it
// forwards. A request carrying one came through a proxy, so the loopback peer
// that delivered it is not the caller.
var forwardingHeaders = []string{"Forwarded", "X-Forwarded-For", "X-Real-Ip"}

// adminOpenLocalOnly guards an admin API that has no credential to check: no
// master key, no managed key, no authentication extension. Such a gateway
// serves the admin API only to callers on its own machine, so a fresh
// install can be set up from the dashboard without being reachable from the
// network. A request authenticated with a managed key passes: once a key
// exists the authentication middleware requires one on every admin route.
func adminOpenLocalOnly(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if _, isManagedKey := managedDashboardAccess(c.Request().Context()); isManagedKey || isLocalCaller(c) {
			return next(c)
		}
		const message = "the admin API accepts unauthenticated requests only from this machine; set GOMODEL_MASTER_KEY to manage the gateway remotely"
		requestlog.EnrichEntryWithError(c, string(core.ErrorTypeAuthentication), message)
		return writeGatewayError(c, (&core.GatewayError{
			Type:       core.ErrorTypeAuthentication,
			Message:    message,
			StatusCode: http.StatusForbidden,
		}).WithCode("admin_remote_access_denied"))
	}
}

// isLocalCaller reports whether the request came from this machine: its
// socket peer is a loopback address, it carries no forwarding header, and the
// configured client address extraction also yields loopback. Only the socket
// peer is trusted; a forwarding header can make a request look remote, never
// local.
func isLocalCaller(c *echo.Context) bool {
	req := c.Request()
	for _, header := range forwardingHeaders {
		if req.Header.Get(header) != "" {
			return false
		}
	}
	return isLoopbackAddress(req.RemoteAddr) && isLoopbackAddress(c.RealIP())
}

func isLoopbackAddress(value string) bool {
	host, _, err := net.SplitHostPort(value)
	if err != nil {
		host = value
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.Unmap().IsLoopback()
}
