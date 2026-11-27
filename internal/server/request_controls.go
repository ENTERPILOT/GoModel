package server

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
)

// requestControls reads the request control headers the operator allowed
// (server.request_control_headers) and attaches what they ask for to the
// request context. A control header that is not in allowed is not read at all,
// so it changes nothing; none of them is forwarded to a provider either way.
//
// It runs after authentication, so only authenticated clients steer the
// gateway, and before workflow resolution, which applies the controls.
func requestControls(allowed []string) echo.MiddlewareFunc {
	failoverAllowed := slices.ContainsFunc(allowed, func(name string) bool {
		return strings.EqualFold(strings.TrimSpace(name), core.FailoverControlHeader)
	})
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			req := c.Request()
			if !core.DescribeEndpoint(req.Method, req.URL.Path).ModelInteraction {
				return next(c)
			}
			var controls core.RequestControls
			if failoverAllowed {
				on, sent, err := controlSwitch(req.Header, core.FailoverControlHeader)
				if err != nil {
					return handleError(c, err)
				}
				controls.FailoverOff = sent && !on
			}
			if controls != (core.RequestControls{}) {
				c.SetRequest(req.WithContext(core.WithRequestControls(req.Context(), controls)))
			}
			return next(c)
		}
	}
}

// controlSwitch reads an on/off request control header. sent is false when
// the request does not carry it. A repeated header is ambiguous (a proxy may
// have added its own), so it is rejected like any other unusable value rather
// than guessed at.
func controlSwitch(header http.Header, name string) (on, sent bool, err error) {
	values := header.Values(name)
	if len(values) == 0 {
		return false, false, nil
	}
	if len(values) == 1 {
		switch strings.ToLower(strings.TrimSpace(values[0])) {
		case "on", "true", "1":
			return true, true, nil
		case "off", "false", "0":
			return false, true, nil
		}
	}
	return false, false, core.NewInvalidRequestError(fmt.Sprintf("invalid %s header: send it once, as on or off", name), nil)
}
