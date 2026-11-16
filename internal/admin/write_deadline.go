package admin

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v5"
)

// clearWriteDeadline lifts the server-wide write timeout from one response.
// It is for the few admin actions that can outlast it: the timeout does not
// stop the handler, it only drops the response, so the dashboard would report
// a failure for work that completed.
func clearWriteDeadline(c *echo.Context) {
	_ = http.NewResponseController(c.Response()).SetWriteDeadline(time.Time{})
}
