// Package admin provides the admin REST API and dashboard for GoModel.
package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/authkeys"
	"gomodel/internal/budget"
	"gomodel/internal/core"
	"gomodel/internal/guardrails"
	"gomodel/internal/live"
	"gomodel/internal/pricingoverrides"
	"gomodel/internal/providers"
	"gomodel/internal/usage"
	"gomodel/internal/virtualmodels"
	"gomodel/internal/workflows"
)

// Handler serves admin API endpoints.
type Handler struct {
	usageReader         usage.UsageReader
	usageRecalculator   usage.PricingRecalculator
	auditReader         auditlog.Reader
	registry            *providers.ModelRegistry
	pricingResolver     usage.PricingResolver
	authKeys            *authkeys.Service
	virtualModels       *virtualmodels.Service
	pricingOverrides    *pricingoverrides.Service
	workflows           *workflows.Service
	budgets             *budget.Service
	guardrails          guardrails.Catalog
	guardrailDefs       *guardrails.Service
	liveBroker          *live.Broker
	runtimeConfig       DashboardConfigResponse
	runtimeRefresher    RuntimeRefresher
	configuredProviders []providers.SanitizedProviderConfig

	mutationMu sync.Mutex
	pricingMu  sync.Mutex
}

// Option configures the admin API handler.
type Option func(*Handler)

const (
	DashboardConfigFeatureFallbackMode  = "FEATURE_FALLBACK_MODE"
	DashboardConfigLoggingEnabled       = "LOGGING_ENABLED"
	DashboardConfigUsageEnabled         = "USAGE_ENABLED"
	DashboardConfigBudgetsEnabled       = "BUDGETS_ENABLED"
	DashboardConfigGuardrailsEnabled    = "GUARDRAILS_ENABLED"
	DashboardConfigCacheEnabled         = "CACHE_ENABLED"
	DashboardConfigRedisURL             = "REDIS_URL"
	DashboardConfigSemanticCacheEnabled = "SEMANTIC_CACHE_ENABLED"
	DashboardConfigPricingRecalculation = "USAGE_PRICING_RECALCULATION_ENABLED"
	DashboardConfigLiveLogsEnabled      = "DASHBOARD_LIVE_LOGS_ENABLED"
)

// statusClientClosedRequest is the de facto status used by proxies for client-aborted requests.
const statusClientClosedRequest = 499

// DashboardConfigResponse is the allowlisted runtime config contract exposed to the dashboard UI.
type DashboardConfigResponse struct {
	FeatureFallbackMode  string `json:"FEATURE_FALLBACK_MODE,omitempty"`
	LoggingEnabled       string `json:"LOGGING_ENABLED,omitempty"`
	UsageEnabled         string `json:"USAGE_ENABLED,omitempty"`
	BudgetsEnabled       string `json:"BUDGETS_ENABLED,omitempty"`
	GuardrailsEnabled    string `json:"GUARDRAILS_ENABLED,omitempty"`
	CacheEnabled         string `json:"CACHE_ENABLED,omitempty"`
	RedisURL             string `json:"REDIS_URL,omitempty"`
	SemanticCacheEnabled string `json:"SEMANTIC_CACHE_ENABLED,omitempty"`
	PricingRecalculation string `json:"USAGE_PRICING_RECALCULATION_ENABLED,omitempty"`
	LiveLogsEnabled      string `json:"DASHBOARD_LIVE_LOGS_ENABLED,omitempty"`
}

type providerStatusSummaryResponse struct {
	Total         int    `json:"total"`
	Healthy       int    `json:"healthy"`
	Degraded      int    `json:"degraded"`
	Unhealthy     int    `json:"unhealthy"`
	OverallStatus string `json:"overall_status"`
}

type providerStatusItemResponse struct {
	Name         string                            `json:"name"`
	Type         string                            `json:"type"`
	Status       string                            `json:"status"`
	StatusLabel  string                            `json:"status_label"`
	StatusReason string                            `json:"status_reason"`
	LastError    string                            `json:"last_error,omitempty"`
	Config       providers.SanitizedProviderConfig `json:"config"`
	Runtime      providers.ProviderRuntimeSnapshot `json:"runtime"`
}

type providerStatusResponse struct {
	Summary   providerStatusSummaryResponse `json:"summary"`
	Providers []providerStatusItemResponse  `json:"providers"`
}

type auditLogEntryResponse struct {
	auditlog.LogEntry
	Usage *usage.RequestUsageSummary `json:"usage,omitempty"`
}

type auditLogListResponse struct {
	Entries []auditLogEntryResponse `json:"entries"`
	Total   int                     `json:"total"`
	Limit   int                     `json:"limit"`
	Offset  int                     `json:"offset"`
}

const (
	RuntimeRefreshStatusOK      = "ok"
	RuntimeRefreshStatusPartial = "partial"
	RuntimeRefreshStatusFailed  = "failed"
	RuntimeRefreshStatusSkipped = "skipped"
)

// RuntimeRefreshStep describes the result of one manual runtime refresh step.
type RuntimeRefreshStep struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

// RuntimeRefreshReport is returned by the manual runtime refresh endpoint.
type RuntimeRefreshReport struct {
	Status        string               `json:"status"`
	StartedAt     time.Time            `json:"started_at"`
	FinishedAt    time.Time            `json:"finished_at"`
	DurationMS    int64                `json:"duration_ms"`
	ModelCount    int                  `json:"model_count"`
	ProviderCount int                  `json:"provider_count"`
	Steps         []RuntimeRefreshStep `json:"steps"`
}

// RuntimeRefresher refreshes application runtime snapshots on demand.
type RuntimeRefresher interface {
	RefreshRuntime(ctx context.Context) (RuntimeRefreshReport, error)
}

// WithAuditReader enables audit log read endpoints.
func WithAuditReader(reader auditlog.Reader) Option {
	return func(h *Handler) {
		h.auditReader = reader
	}
}

// WithUsagePricingRecalculator enables persisted usage pricing recalculation.
func WithUsagePricingRecalculator(recalculator usage.PricingRecalculator) Option {
	return func(h *Handler) {
		h.usageRecalculator = recalculator
	}
}

// WithPricingResolver sets the resolver used for usage pricing recalculation.
func WithPricingResolver(resolver usage.PricingResolver) Option {
	return func(h *Handler) {
		h.pricingResolver = resolver
	}
}

// WithVirtualModels enables unified virtual model administration endpoints.
func WithVirtualModels(service *virtualmodels.Service) Option {
	return func(h *Handler) {
		h.virtualModels = service
	}
}

// WithAuthKeys enables managed auth key administration endpoints.
func WithAuthKeys(service *authkeys.Service) Option {
	return func(h *Handler) {
		h.authKeys = service
	}
}

// WithPricingOverrides enables model pricing override administration endpoints.
func WithPricingOverrides(service *pricingoverrides.Service) Option {
	return func(h *Handler) {
		h.pricingOverrides = service
	}
}

// WithWorkflows enables workflow administration endpoints.
func WithWorkflows(service *workflows.Service) Option {
	return func(h *Handler) {
		h.workflows = service
	}
}

// WithBudgets enables budget administration endpoints.
func WithBudgets(service *budget.Service) Option {
	return func(h *Handler) {
		h.budgets = service
	}
}

// WithGuardrailsRegistry enables listing valid guardrail references for workflow authoring.
func WithGuardrailsRegistry(registry guardrails.Catalog) Option {
	return func(h *Handler) {
		h.guardrails = registry
	}
}

// WithGuardrailService enables full guardrail definition administration endpoints.
func WithGuardrailService(service *guardrails.Service) Option {
	return func(h *Handler) {
		h.guardrails = service
		h.guardrailDefs = service
	}
}

// WithLiveBroker enables realtime dashboard log previews.
func WithLiveBroker(broker *live.Broker) Option {
	return func(h *Handler) {
		h.liveBroker = broker
	}
}

// WithDashboardRuntimeConfig enables the allowlisted dashboard runtime config endpoint.
func WithDashboardRuntimeConfig(values DashboardConfigResponse) Option {
	return func(h *Handler) {
		h.runtimeConfig = normalizeDashboardRuntimeConfig(values)
	}
}

// WithRuntimeRefresher enables manual runtime refresh from the admin API.
func WithRuntimeRefresher(refresher RuntimeRefresher) Option {
	return func(h *Handler) {
		h.runtimeRefresher = refresher
	}
}

// WithConfiguredProviders enables the admin-safe provider inventory endpoint.
func WithConfiguredProviders(configs []providers.SanitizedProviderConfig) Option {
	return func(h *Handler) {
		h.configuredProviders = cloneConfiguredProviders(configs)
	}
}

// NewHandler creates a new admin API handler.
// usageReader may be nil if usage tracking is not available.
func NewHandler(reader usage.UsageReader, registry *providers.ModelRegistry, options ...Option) *Handler {
	h := &Handler{
		usageReader:   reader,
		registry:      registry,
		runtimeConfig: DashboardConfigResponse{},
	}
	if registry != nil {
		h.pricingResolver = registry
	}

	for _, opt := range options {
		if opt != nil {
			opt(h)
		}
	}

	return h
}

func normalizeDashboardRuntimeConfig(values DashboardConfigResponse) DashboardConfigResponse {
	return DashboardConfigResponse{
		FeatureFallbackMode:  strings.TrimSpace(values.FeatureFallbackMode),
		LoggingEnabled:       strings.TrimSpace(values.LoggingEnabled),
		UsageEnabled:         strings.TrimSpace(values.UsageEnabled),
		BudgetsEnabled:       strings.TrimSpace(values.BudgetsEnabled),
		GuardrailsEnabled:    strings.TrimSpace(values.GuardrailsEnabled),
		CacheEnabled:         strings.TrimSpace(values.CacheEnabled),
		RedisURL:             strings.TrimSpace(values.RedisURL),
		SemanticCacheEnabled: strings.TrimSpace(values.SemanticCacheEnabled),
		PricingRecalculation: strings.TrimSpace(values.PricingRecalculation),
		LiveLogsEnabled:      strings.TrimSpace(values.LiveLogsEnabled),
	}
}

func cloneDashboardRuntimeConfig(values DashboardConfigResponse) DashboardConfigResponse {
	return values
}

func cloneConfiguredProviders(configs []providers.SanitizedProviderConfig) []providers.SanitizedProviderConfig {
	if len(configs) == 0 {
		return nil
	}
	cloned := make([]providers.SanitizedProviderConfig, len(configs))
	for i := range configs {
		cloned[i] = configs[i]
		if len(configs[i].Models) > 0 {
			cloned[i].Models = append([]string(nil), configs[i].Models...)
		}
	}
	return cloned
}

var validIntervals = map[string]bool{
	"daily":   true,
	"weekly":  true,
	"monthly": true,
	"yearly":  true,
}

const (
	dashboardTimeZoneHeader = "X-GoModel-Timezone"
	defaultDashboardTZ      = "UTC"
	defaultDateRangeDays    = 30
	maxDateRangeDays        = 365
)

var timeNow = time.Now

// parseUsageParams extracts UsageQueryParams from the request query string.
// Returns an error if date parameters are provided but malformed.
func parseUsageParams(c *echo.Context) (usage.UsageQueryParams, error) {
	params, err := parseDateRangeParams(c)
	if err != nil {
		return params, err
	}

	// Parse interval
	params.Interval = c.QueryParam("interval")
	if !validIntervals[params.Interval] {
		params.Interval = "daily"
	}
	params.CacheMode = c.QueryParam("cache_mode")

	userPath, err := normalizeUserPathQueryParam("user_path", c.QueryParam("user_path"))
	if err != nil {
		return params, err
	}
	params.UserPath = userPath

	return params, nil
}

func normalizeUserPathQueryParam(fieldName, raw string) (string, error) {
	userPath, err := core.NormalizeUserPath(raw)
	if err != nil {
		return "", core.NewInvalidRequestError("invalid "+fieldName+": "+err.Error(), err)
	}
	return userPath, nil
}

// parseDateRangeParams extracts common date range query params.
// Returns an error if date parameters are provided but malformed.
func parseDateRangeParams(c *echo.Context) (usage.UsageQueryParams, error) {
	var params usage.UsageQueryParams

	timeZone, location := dashboardTimeZone(c)
	params.TimeZone = timeZone

	now := timeNow().In(location)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)

	days := defaultDateRangeDays
	if d := c.QueryParam("days"); d != "" {
		if parsed, err := strconv.Atoi(d); err == nil && parsed > 0 {
			days = min(parsed, maxDateRangeDays)
		}
	}

	start, end, err := buildDateRange(strings.TrimSpace(c.QueryParam("start_date")), strings.TrimSpace(c.QueryParam("end_date")), days, location, today)
	if err != nil {
		return params, err
	}
	params.StartDate = start
	params.EndDate = end
	return params, nil
}

func buildDateRange(startStr, endStr string, days int, location *time.Location, today time.Time) (time.Time, time.Time, error) {
	var start, end time.Time
	var startParsed, endParsed bool

	if startStr != "" {
		t, err := time.ParseInLocation("2006-01-02", startStr, location)
		if err != nil {
			return time.Time{}, time.Time{}, core.NewInvalidRequestError("invalid start_date format, expected YYYY-MM-DD", nil)
		}
		start = t
		startParsed = true
	}
	if endStr != "" {
		t, err := time.ParseInLocation("2006-01-02", endStr, location)
		if err != nil {
			return time.Time{}, time.Time{}, core.NewInvalidRequestError("invalid end_date format, expected YYYY-MM-DD", nil)
		}
		end = t
		endParsed = true
	}

	if startParsed || endParsed {
		if !startParsed {
			start = end.AddDate(0, 0, -29)
		}
		if !endParsed {
			end = today
		}
	} else {
		days = normalizeDateRangeDays(days)
		end = today
		start = today.AddDate(0, 0, -(days - 1))
	}

	if start.After(end) {
		return time.Time{}, time.Time{}, core.NewInvalidRequestError("start_date must be on or before end_date", nil)
	}
	return start, end, nil
}

func normalizeDateRangeDays(days int) int {
	if days <= 0 {
		return defaultDateRangeDays
	}
	return min(days, maxDateRangeDays)
}

func dashboardTimeZone(c *echo.Context) (string, *time.Location) {
	value := strings.TrimSpace(c.Request().Header.Get(dashboardTimeZoneHeader))
	if value == "" {
		return defaultDashboardTZ, time.UTC
	}

	location, err := time.LoadLocation(value)
	if err != nil {
		return defaultDashboardTZ, time.UTC
	}

	return location.String(), location
}

// handleError converts errors to appropriate HTTP responses, matching the
// format used by the main API handlers in the server package.
func handleError(c *echo.Context, err error) error {
	if gatewayErr, ok := errors.AsType[*core.GatewayError](err); ok {
		logHandledAdminError(c, gatewayErr)
		return c.JSON(gatewayErr.HTTPStatusCode(), gatewayErr.ToJSON())
	}

	if errors.Is(err, context.Canceled) {
		gatewayErr := core.NewInvalidRequestErrorWithStatus(statusClientClosedRequest, "request canceled", err).
			WithCode("request_canceled")
		logHandledAdminError(c, gatewayErr)
		return c.JSON(gatewayErr.HTTPStatusCode(), gatewayErr.ToJSON())
	}
	if errors.Is(err, context.DeadlineExceeded) {
		gatewayErr := core.NewInvalidRequestErrorWithStatus(http.StatusGatewayTimeout, "request timed out", err).
			WithCode("request_timeout")
		logHandledAdminError(c, gatewayErr)
		return c.JSON(gatewayErr.HTTPStatusCode(), gatewayErr.ToJSON())
	}

	fallback := &core.GatewayError{
		Type:       "internal_error",
		Message:    "an unexpected error occurred",
		StatusCode: http.StatusInternalServerError,
		Err:        err,
	}
	logHandledAdminError(c, fallback)
	return c.JSON(fallback.HTTPStatusCode(), fallback.ToJSON())
}

func logHandledAdminError(c *echo.Context, gatewayErr *core.GatewayError) {
	if gatewayErr == nil {
		return
	}

	attrs := []any{
		"type", gatewayErr.Type,
		"status", gatewayErr.HTTPStatusCode(),
		"message", gatewayErr.Message,
	}
	if gatewayErr.Provider != "" {
		attrs = append(attrs, "provider", gatewayErr.Provider)
	}
	if gatewayErr.Param != nil {
		attrs = append(attrs, "param", *gatewayErr.Param)
	}
	if gatewayErr.Code != nil {
		attrs = append(attrs, "code", *gatewayErr.Code)
	}
	if gatewayErr.Err != nil {
		attrs = append(attrs, "error", gatewayErr.Err)
	}
	if c != nil && c.Request() != nil {
		req := c.Request()
		attrs = append(attrs,
			"method", req.Method,
			"path", req.URL.Path,
		)
		if requestID := requestIDFromAdminContextOrHeader(req); requestID != "" {
			attrs = append(attrs, "request_id", requestID)
		}
	}

	status := gatewayErr.HTTPStatusCode()
	if status == statusClientClosedRequest {
		slog.Debug("admin request canceled", attrs...)
		return
	}
	if status >= http.StatusInternalServerError {
		slog.Error("admin request failed", attrs...)
		return
	}
	slog.Warn("admin request failed", attrs...)
}

func requestIDFromAdminContextOrHeader(req *http.Request) string {
	if req == nil {
		return ""
	}
	if requestID := strings.TrimSpace(core.GetRequestID(req.Context())); requestID != "" {
		return requestID
	}
	return strings.TrimSpace(req.Header.Get("X-Request-ID"))
}
