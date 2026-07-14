package admin

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/health"
)

func (h *Handler) ProviderStatus(c *echo.Context) error {
	return c.JSON(http.StatusOK, h.buildProviderStatusResponse())
}

// RefreshRuntime handles POST /admin/runtime/refresh
func (h *Handler) RefreshRuntime(c *echo.Context) error {
	if h.runtimeRefresher == nil {
		return handleError(c, featureUnavailableError("runtime refresh is unavailable"))
	}

	report, err := h.runtimeRefresher.RefreshRuntime(c.Request().Context())
	if err != nil {
		if gatewayErr, ok := errors.AsType[*core.GatewayError](err); ok {
			return handleError(c, gatewayErr)
		}
		return handleError(c, core.NewProviderError("runtime_refresh", http.StatusInternalServerError, "runtime refresh failed", err))
	}
	if report.Status == "" {
		report.Status = RuntimeRefreshStatusOK
	}
	if report.Steps == nil {
		report.Steps = []RuntimeRefreshStep{}
	}
	return c.JSON(http.StatusOK, report)
}

func (h *Handler) buildProviderStatusResponse() providerStatusResponse {
	configuredByName, runtimeByName, names := h.collectProviderStatusInputs()

	var healthByName map[string]health.ProviderHealth
	if h.requestHealth != nil {
		healthByName = h.requestHealth.Snapshot()
	}

	resp := providerStatusResponse{
		Summary: providerStatusSummaryResponse{
			OverallStatus: "degraded",
		},
		Providers: make([]providerStatusItemResponse, 0, len(names)),
	}

	for _, name := range names {
		item := buildProviderStatusItem(name, configuredByName[name], runtimeByName[name], requestHealthFor(healthByName, name))
		resp.Providers = append(resp.Providers, item)
		resp.Summary.Total++
		switch item.Status {
		case "healthy":
			resp.Summary.Healthy++
		case "unhealthy":
			resp.Summary.Unhealthy++
		default:
			resp.Summary.Degraded++
		}
	}

	resp.Summary.OverallStatus = overallProviderStatus(resp.Summary)
	return resp
}

// collectProviderStatusInputs merges the configured-provider snapshot with the
// registry's runtime snapshots and returns lookups keyed by trimmed name plus
// a deterministically sorted list of all known names.
func (h *Handler) collectProviderStatusInputs() (
	map[string]providers.SanitizedProviderConfig,
	map[string]providers.ProviderRuntimeSnapshot,
	[]string,
) {
	configured := cloneConfiguredProviders(h.configuredProviders)
	configuredByName := make(map[string]providers.SanitizedProviderConfig, len(configured))
	nameSet := make(map[string]struct{}, len(configured))
	for _, cfg := range configured {
		name := strings.TrimSpace(cfg.Name)
		if name == "" {
			continue
		}
		configuredByName[name] = cfg
		nameSet[name] = struct{}{}
	}

	runtimeByName := make(map[string]providers.ProviderRuntimeSnapshot)
	if h.registry != nil {
		for _, snapshot := range h.registry.ProviderRuntimeSnapshots() {
			name := strings.TrimSpace(snapshot.Name)
			if name == "" {
				continue
			}
			runtimeByName[name] = snapshot
			nameSet[name] = struct{}{}
		}
	}

	names := make([]string, 0, len(nameSet))
	for name := range nameSet {
		names = append(names, name)
	}
	sort.Strings(names)
	return configuredByName, runtimeByName, names
}

// buildProviderStatusItem reconciles cfg/runtime gaps for a single provider
// (either side may be zero-valued when only one source knows the name) and
// produces the response row.
func buildProviderStatusItem(name string, cfg providers.SanitizedProviderConfig, runtime providers.ProviderRuntimeSnapshot, requestHealth *health.ProviderHealth) providerStatusItemResponse {
	// Classify against the inputs as-given so the "Unknown" branch in
	// classifyProviderStatus stays reachable for runtime-only providers.
	// Synthesising cfg.Name first would always make the provider look
	// configured to the classifier.
	status, label, reason, lastError := classifyProviderStatus(cfg, runtime)
	status, label, reason, lastError = applyRequestHealth(status, label, reason, lastError, requestHealth)

	// For the response row, fill in display fallbacks from the peer side.
	if strings.TrimSpace(cfg.Name) == "" {
		cfg = providers.SanitizedProviderConfig{Name: name, Type: strings.TrimSpace(runtime.Type)}
	}
	if strings.TrimSpace(runtime.Name) == "" {
		runtime = providers.ProviderRuntimeSnapshot{Name: name, Type: strings.TrimSpace(cfg.Type)}
	}
	if strings.TrimSpace(cfg.Type) == "" {
		cfg.Type = strings.TrimSpace(runtime.Type)
	}
	if strings.TrimSpace(runtime.Type) == "" {
		runtime.Type = strings.TrimSpace(cfg.Type)
	}

	return providerStatusItemResponse{
		Name:          name,
		Type:          strings.TrimSpace(cfg.Type),
		Status:        status,
		StatusLabel:   label,
		StatusReason:  reason,
		LastError:     lastError,
		Config:        cfg,
		Runtime:       runtime,
		RequestHealth: requestHealth,
	}
}

// requestHealthFor matches a status row (keyed by trimmed provider name) to
// its health snapshot; snapshot keys are trimmed too since llmclient records
// the configured name as-is.
func requestHealthFor(healthByName map[string]health.ProviderHealth, name string) *health.ProviderHealth {
	for key, snapshot := range healthByName {
		if strings.TrimSpace(key) == name {
			return &snapshot
		}
	}
	return nil
}

// applyRequestHealth folds real-traffic signals (circuit breaker state and
// flagged models) into the discovery-based classification. Signals only
// worsen a status, never improve it, and an equally severe base status keeps
// its more specific label and reason.
func applyRequestHealth(status, label, reason, lastError string, rh *health.ProviderHealth) (string, string, string, string) {
	if rh == nil {
		return status, label, reason, lastError
	}

	flagged := rh.FlaggedModels()
	switch rh.CircuitState {
	case "open":
		if status != "unhealthy" {
			status, label = "unhealthy", "Circuit Open"
			reason = "circuit breaker is open; recent requests to this provider failed and traffic is paused"
		}
	case "half-open":
		if status == "healthy" {
			status, label = "degraded", "Recovering"
			reason = "circuit breaker is half-open; probing whether the provider has recovered"
		}
	}
	if len(flagged) > 0 && status == "healthy" {
		status, label = "degraded", "Degraded"
		reason = "recent requests are failing for: " + strings.Join(flagged, ", ")
	}
	if lastError == "" {
		lastError = latestModelError(rh)
	}
	return status, label, reason, lastError
}

// latestModelError surfaces the most recent per-model failure so the card can
// show what real traffic is hitting even when discovery reports no error. It
// uses the provider-level LastError, which the tracker computes across every
// tracked model before the snapshot's model list is capped.
func latestModelError(rh *health.ProviderHealth) string {
	if rh.LastError == nil {
		return ""
	}
	if rh.LastErrorModel == "" {
		return rh.LastError.Message
	}
	return rh.LastErrorModel + ": " + rh.LastError.Message
}

func overallProviderStatus(summary providerStatusSummaryResponse) string {
	switch {
	case summary.Total == 0:
		return "degraded"
	case summary.Healthy == summary.Total:
		return "healthy"
	case summary.Unhealthy == summary.Total:
		return "unhealthy"
	default:
		return "degraded"
	}
}

func classifyProviderStatus(cfg providers.SanitizedProviderConfig, runtime providers.ProviderRuntimeSnapshot) (status, label, reason, lastError string) {
	modelFetchError := strings.TrimSpace(runtime.LastModelFetchError)
	availabilityError := strings.TrimSpace(runtime.LastAvailabilityError)
	configuredName := strings.TrimSpace(cfg.Name)
	usingCachedModels := runtime.Registered &&
		runtime.DiscoveredModelCount > 0 &&
		modelFetchError == "" &&
		runtime.LastModelFetchSuccessAt == nil

	lastError = modelFetchError
	if lastError == "" {
		lastError = availabilityError
	}

	switch {
	case runtime.DiscoveredModelCount > 0 && modelFetchError == "":
		if runtime.InventoryStale {
			// An availability probe failed without a model fetch running, so
			// the inventory was retired from load balancing while the fetch
			// error stayed empty. Surfacing "healthy" here would contradict
			// the routing behavior.
			return "degraded", "Degraded", "latest availability probe failed; previous inventory is still available", lastError
		}
		if usingCachedModels {
			return "degraded", "Starting", "serving cached model inventory while live refresh finishes", lastError
		}
		return "healthy", "Healthy", "configured and model discovery succeeded", lastError
	case modelFetchError != "" && runtime.DiscoveredModelCount > 0:
		return "degraded", "Degraded", "latest model refresh failed; previous inventory is still available", lastError
	case modelFetchError != "":
		return "unhealthy", "Unhealthy", "model discovery failed and no provider models are currently available", lastError
	case availabilityError != "" && runtime.DiscoveredModelCount == 0:
		return "unhealthy", "Unhealthy", "startup availability check failed and no provider models are available", lastError
	case runtime.DiscoveredModelCount > 0:
		return "healthy", "Healthy", "provider models are currently available", lastError
	case !runtime.Registered && configuredName != "":
		return "degraded", "Starting", "provider is configured and awaiting live model discovery", lastError
	case configuredName != "":
		return "degraded", "Configured", "provider is configured but has not exposed models yet", lastError
	default:
		return "degraded", "Unknown", "provider runtime inventory is unavailable", lastError
	}
}
