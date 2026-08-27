package server

import (
	"context"
	"net/http"
	"time"

	"github.com/enterpilot/gomodel/internal/version"
	"github.com/enterpilot/gomodel/internal/versioncheck"

	"github.com/labstack/echo/v5"
)

// Version handles GET /version.
//
// It always answers from the gateway's cached result and never waits on the
// network: a due check is dispatched in the background and lands in the cache
// for the next caller. The first visit of each day — the
// browser's gomodel_version_check cookie carries a date older than today —
// also refreshes that cache, forwarding an allowlisted slice of the visit
// (user agent, language, client hints, visit marker) to the release host.
// Credentials, client addresses, and the dashboard's own hostname are never
// forwarded; see versioncheck.Beacon.
//
// The response re-stamps the cookie with today's date, keeping the browser's
// id, so the next visit today skips the check.
//
// @Summary      Report the running version and whether an update exists
// @Tags         health
// @Produce      json
// @Success      200  {object}  versioncheck.Status
// @Router       /version [get]
func (h *Handler) Version(c *echo.Context) error {
	checker := h.versionChecker
	if checker == nil {
		return c.JSON(http.StatusOK, versioncheck.Status{
			App:     version.App,
			Version: version.Version,
		})
	}

	request := c.Request()
	now := time.Now()
	visit := ""
	if cookie, err := request.Cookie(versioncheck.CookieName); err == nil {
		visit = cookie.Value
	}
	_, id := versioncheck.SplitVisit(visit)

	status := checker.Status()
	if versioncheck.DueToday(visit, now) {
		next := versioncheck.NewVisit(id, now)
		if checker.Enabled() {
			// Detached from the request: the refresh must outlive the
			// response, and the dashboard must never wait on the release
			// host. A slow or unreachable manifest would otherwise stall
			// this endpoint for the checker's whole timeout. The result
			// lands in the cache for the next caller; Refresh applies its
			// own timeout, throttles, and budget.
			// The beacon is snapshotted here on purpose: echo recycles the
			// request once the handler returns, so nothing derived from it
			// may be read inside the goroutine. Background context because
			// the refresh outlives the response and needs no request-scoped
			// values; Refresh applies its own timeout.
			beacon := versioncheck.BeaconFromRequest(request, next)
			go func() {
				_, _ = checker.Refresh(context.Background(), beacon)
			}()
		}
		// Stamped even when no check ran, so a deployment that opted out
		// still gets a well-formed marker instead of the dashboard treating
		// every load as its first of the day.
		h.setVisitCookie(c, next)
	}

	// The cookie decides whether this request reached the release host, and
	// the answer changes daily, so no proxy in between may cache it.
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, status)
}

// setVisitCookie writes the YYYY-MM-DD-{id} visit marker. Secure is set only
// on HTTPS requests: a self-hosted dashboard reached over plain HTTP on
// localhost would otherwise have the cookie silently dropped, and the daily
// gate would degrade into a check on every page load.
func (h *Handler) setVisitCookie(c *echo.Context, value string) {
	http.SetCookie(c.Response(), &http.Cookie{
		Name:     versioncheck.CookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   versioncheck.CookieMaxAge,
		Secure:   c.IsTLS(),
		SameSite: http.SameSiteLaxMode,
		// Not HttpOnly on purpose: the dashboard reads the date half to skip
		// the request once it has already checked in today.
		HttpOnly: false,
	})
}
