package server

import (
	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
)

// requireClientCertificate enforces mTLS on every request except the health
// probes. The listener verifies any client certificate presented but lets a
// handshake without one complete, so the kubelet's HTTPS probes and
// `gomodel --health`, which present none, still reach /health and
// /health/ready. The paths are matched exactly, after the base path is
// stripped, so nothing else answers without a verified certificate. The match
// is on the escaped path, which the router also uses, so an encoded form such
// as /%68ealth cannot reach a catch-all route without a certificate.
func requireClientCertificate(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		req := c.Request()
		if req.TLS != nil && len(req.TLS.VerifiedChains) > 0 {
			return next(c)
		}
		switch req.URL.EscapedPath() {
		case "/health", "/health/ready":
			return next(c)
		}
		return handleError(c, core.NewAuthenticationError("", "a client certificate is required").WithCode("client_certificate_required"))
	}
}
