package admin

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/labstack/echo/v5"
)

// clearWriteDeadline lifts the server-wide write timeout from one response.
// It is for the few admin actions that can outlast it: the timeout does not
// stop the handler, it only drops the response, so the dashboard would report
// a failure for work that completed.
func clearWriteDeadline(c *echo.Context) {
	err := http.NewResponseController(c.Response()).SetWriteDeadline(time.Time{})
	if err != nil && !errors.Is(err, http.ErrNotSupported) {
		slog.Warn("failed to clear write deadline for a long admin action",
			"route", c.Path(),
			"error", err,
		)
	}
}
