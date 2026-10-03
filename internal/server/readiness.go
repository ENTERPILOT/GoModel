package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/ext"
)

// readinessProbeTimeout caps each dependency check. It is intentionally shorter
// than the CLI --ready timeout (and the Docker/orchestrator probe timeout) so a
// slow dependency yields a clean not_ready/degraded response instead of the
// client cutting the connection on its own timeout.
const readinessProbeTimeout = 2 * time.Second

// Readiness component and status values.
const (
	readyStatusReady    = "ready"
	readyStatusDegraded = "degraded"
	readyStatusNotReady = "not_ready"

	readyComponentOK       = "ok"
	readyComponentDegraded = "degraded"
	readyComponentDown     = "down"
)

// coreReadyComponents are the component keys core reports itself; extension
// health checkers cannot claim them.
var coreReadyComponents = []string{"storage", "cache", "models"}

// readinessResponse is the JSON body returned by GET /health/ready.
type readinessResponse struct {
	Status     string            `json:"status"`
	Components map[string]string `json:"components,omitempty"`
}

// Ready handles GET /health/ready
//
// Readiness reports whether this instance should receive traffic. It probes
// dependencies the gateway owns:
//   - Storage is required: if it is unreachable the gateway cannot serve
//     requests, so the response is not_ready (HTTP 503).
//   - The Redis exact cache is a performance optimization: if it is unreachable
//     the gateway still serves requests, so the response is degraded (HTTP 200).
//   - The model registry: with providers configured but no models loaded (model
//     discovery has not succeeded and nothing is cached), requests fail with
//     503 until it does. The response is degraded (HTTP 200) so the condition
//     is visible without taking the instance out of rotation.
//   - Extension health checkers report under their own name: degraded keeps
//     HTTP 200, down makes the response not_ready (HTTP 503). They run
//     concurrently under one shared deadline; one that does not answer in
//     time reports degraded.
//
// Upstream provider reachability is deliberately excluded — a provider outage
// must not pull a healthy gateway out of rotation. Use GET /health for liveness.
//
// @Summary      Readiness check
// @Tags         system
// @Produce      json
// @Success      200  {object}  map[string]interface{}  "ready or degraded"
// @Failure      503  {object}  map[string]interface{}  "not ready"
// @Router       /health/ready [get]
func (h *Handler) Ready(c *echo.Context) error {
	components := map[string]string{}
	status := readyStatusReady
	// Extension checks run concurrently with the core probes under one shared
	// deadline, so they add no latency beyond readinessProbeTimeout however
	// many are registered.
	waitExtensionHealth := startExtensionHealthChecks(c.Request().Context(), h.healthCheckers)

	if h.storageProbe != nil {
		if err := pingWithTimeout(c.Request().Context(), h.storageProbe); err != nil {
			components["storage"] = readyComponentDown
			status = readyStatusNotReady
			slog.Warn("readiness: storage probe failed", "error", err)
		} else {
			components["storage"] = readyComponentOK
		}
	}

	if h.cacheProbe != nil {
		if err := pingWithTimeout(c.Request().Context(), h.cacheProbe); err != nil {
			components["cache"] = readyComponentDown
			if status == readyStatusReady {
				status = readyStatusDegraded
			}
			slog.Warn("readiness: cache probe failed", "error", err)
		} else {
			components["cache"] = readyComponentOK
		}
	}

	if h.modelInventory != nil && h.modelInventory.ProviderCount() > 0 {
		if h.modelInventory.ModelCount() == 0 {
			components["models"] = readyComponentDown
			if status == readyStatusReady {
				status = readyStatusDegraded
			}
		} else {
			components["models"] = readyComponentOK
		}
	}

	extensionHealth := waitExtensionHealth()
	for _, hc := range h.healthCheckers {
		switch extensionHealth[hc.name] {
		case ext.HealthOK:
			components[hc.name] = readyComponentOK
		case ext.HealthDown:
			components[hc.name] = readyComponentDown
			status = readyStatusNotReady
			slog.Warn("readiness: extension reports down", "component", hc.name)
		default:
			components[hc.name] = readyComponentDegraded
			if status == readyStatusReady {
				status = readyStatusDegraded
			}
		}
	}

	code := http.StatusOK
	if status == readyStatusNotReady {
		code = http.StatusServiceUnavailable
	}
	return c.JSON(code, readinessResponse{Status: status, Components: components})
}

// pingWithTimeout runs a readiness probe with a bounded timeout, also honoring
// cancellation of the request context (whichever fires first).
func pingWithTimeout(ctx context.Context, probe ReadinessProbe) error {
	ctx, cancel := context.WithTimeout(ctx, readinessProbeTimeout)
	defer cancel()
	return probe.Ping(ctx)
}
