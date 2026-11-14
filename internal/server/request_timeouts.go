package server

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/enterpilot/gomodel/internal/httpclient"
	"github.com/labstack/echo/v5"
)

const requestTimeoutAuthenticatedKey = "gomodel.requestTimeoutAuthenticated"

// requestTimeoutOverrides consumes gateway-only timeout headers after authentication.
func requestTimeoutOverrides() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			req := c.Request()
			var overrides httpclient.RequestTimeouts
			for _, field := range []struct {
				name   string
				target **time.Duration
			}{
				{"X-GoModel-Request-Timeout-Ms", &overrides.Timeout},
				{"X-GoModel-Response-Header-Timeout-Ms", &overrides.ResponseHeaderTimeout},
			} {
				values := req.Header.Values(field.name)
				if len(values) == 0 {
					continue
				}
				if len(values) != 1 {
					return echo.NewHTTPError(http.StatusBadRequest, "duplicate timeout header")
				}
				ms, err := strconv.ParseUint(values[0], 10, 63)
				if err != nil || ms > uint64((1<<63-1)/int64(time.Millisecond)) {
					return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("%s must be non-negative integer milliseconds", field.name))
				}
				duration := time.Duration(ms) * time.Millisecond
				*field.target = &duration
				req.Header.Del(field.name)
			}
			authenticated, _ := c.Get(requestTimeoutAuthenticatedKey).(bool)
			if authenticated && (overrides.Timeout != nil || overrides.ResponseHeaderTimeout != nil) {
				c.SetRequest(req.WithContext(httpclient.WithRequestTimeouts(req.Context(), overrides)))
			}
			return next(c)
		}
	}
}
