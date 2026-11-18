package server

import (
	"github.com/labstack/echo/v5"
)

// strictTransportSecurityValue tells browsers to reach this host over HTTPS
// only, for 180 days. It leaves out includeSubDomains and preload: the gateway
// cannot speak for other hosts, and a short-lived policy is easy to undo if
// the operator moves TLS back to a proxy.
const strictTransportSecurityValue = "max-age=15552000"

// strictTransportSecurity sends HSTS on responses the gateway serves over its
// own TLS listener. A plain HTTP response must not carry it (RFC 6797
// section 7.2); behind a TLS-terminating proxy, the proxy owns the policy.
func strictTransportSecurity(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if c.Request().TLS != nil {
			c.Response().Header().Set("Strict-Transport-Security", strictTransportSecurityValue)
		}
		return next(c)
	}
}
