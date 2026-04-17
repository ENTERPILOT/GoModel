// Package admin provides the admin REST API and dashboard for GoModel.
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v5"

	"gomodel/internal/aliases"
	"gomodel/internal/auditlog"
	"gomodel/internal/authkeys"
	"gomodel/internal/core"
	"gomodel/internal/guardrails"
	"gomodel/internal/modeloverrides"
	"gomodel/internal/providers"
	"gomodel/internal/usage"
	"gomodel/internal/workflows"
)

// Handler serves admin API endpoints.
type Handler struct {
	usageReader         usage.UsageReader
	auditReader         auditlog.Reader
	registry            *providers.ModelRegistry
	authKeys            *authkeys.Service
	aliases             *aliases.Service
	modelOverrides      *modeloverrides.Service
	workflows           *workflows.Service
	guardrails          guardrails.Catalog
	guardrailDefs       *guardrails.Service
	runtimeConfig       DashboardConfigResponse
	runtimeRefresher    RuntimeRefresher
	configuredProviders []providers.SanitizedProviderConfig

	mutationMu sync.Mutex
}

// Option configures the admin API handler.
type Option func(*Handler)

const (
	DashboardConfigFeatureFallbackMode  = "FEATURE_FALLBACK_MODE"
	DashboardConfigLoggingEnabled       = "LOGGING_ENABLED"
	DashboardConfigUsageEnabled         = "USAGE_ENABLED"
	DashboardConfigGuardrailsEnabled    = "GUARDRAILS_ENABLED"
	DashboardConfigCacheEnabled         = "CACHE_ENABLED"
	DashboardConfigRedisURL             = "REDIS_URL"
	DashboardConfigSemanticCacheEnabled = "SEMANTIC_CACHE_ENABLED"
)

// DashboardConfigResponse is the allowlisted runtime config contract exposed to the dashboard UI.
type DashboardConfigResponse struct {
	FeatureFallbackMode  string `json:"FEATURE_FALLBACK_MODE,omitempty"`
	LoggingEnabled       string `json:"LOGGING_ENABLED,omitempty"`
	UsageEnabled         string `json:"USAGE_ENABLED,omitempty"`
	GuardrailsEnabled    string `json:"GUARDRAILS_ENABLED,omitempty"`
	CacheEnabled         string `json:"CACHE_ENABLED,omitempty"`
	RedisURL             string `json:"REDIS_URL,omitempty"`
	SemanticCacheEnabled string `json:"SEMANTIC_CACHE_ENABLED,omitempty"`
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

// WithAliases enables alias administration endpoints.
func WithAliases(service *aliases.Service) Option {
	return func(h *Handler) {
		h.aliases = service
	}
}

// WithAuthKeys enables managed auth key administration endpoints.
func WithAuthKeys(service *authkeys.Service) Option {
	return func(h *Handler) {
		h.authKeys = service
	}
}

// WithModelOverrides enables model override administration endpoints.
func WithModelOverrides(service *modeloverrides.Service) Option {
	return func(h *Handler) {
		h.modelOverrides = service
	}
}

// WithWorkflows enables workflow administration endpoints.
func WithWorkflows(service *workflows.Service) Option {
	return func(h *Handler) {
		h.workflows = service
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
		GuardrailsEnabled:    strings.TrimSpace(values.GuardrailsEnabled),
		CacheEnabled:         strings.TrimSpace(values.CacheEnabled),
		RedisURL:             strings.TrimSpace(values.RedisURL),
		SemanticCacheEnabled: strings.TrimSpace(values.SemanticCacheEnabled),
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

	startStr := c.QueryParam("start_date")
	endStr := c.QueryParam("end_date")

	var startParsed, endParsed bool

	if startStr != "" {
		t, err := time.ParseInLocation("2006-01-02", startStr, location)
		if err != nil {
			return params, core.NewInvalidRequestError("invalid start_date format, expected YYYY-MM-DD", nil)
		}
		params.StartDate = t
		startParsed = true
	}

	if endStr != "" {
		t, err := time.ParseInLocation("2006-01-02", endStr, location)
		if err != nil {
			return params, core.NewInvalidRequestError("invalid end_date format, expected YYYY-MM-DD", nil)
		}
		params.EndDate = t
		endParsed = true
	}

	if startParsed || endParsed {
		if !startParsed {
			params.StartDate = params.EndDate.AddDate(0, 0, -29)
		}
		if !endParsed {
			params.EndDate = today
		}
		return params, nil
	}

	days := 30
	if d := c.QueryParam("days"); d != "" {
		if parsed, err := strconv.Atoi(d); err == nil && parsed > 0 {
			days = parsed
		}
	}
	params.EndDate = today
	params.StartDate = today.AddDate(0, 0, -(days - 1))

	return params, nil
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
		return c.JSON(gatewayErr.HTTPStatusCode(), gatewayErr.ToJSON())
	}

	fallback := &core.GatewayError{
		Type:       "internal_error",
		Message:    "an unexpected error occurred",
		StatusCode: http.StatusInternalServerError,
		Err:        err,
	}
	return c.JSON(fallback.HTTPStatusCode(), fallback.ToJSON())
}

// UsageSummary handles GET /admin/api/v1/usage/summary
//
// @Summary      Get usage summary
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        days        query     int     false  "Number of days (default 30)"
// @Param        start_date  query     string  false  "Start date (YYYY-MM-DD)"
// @Param        end_date    query     string  false  "End date (YYYY-MM-DD)"
// @Param        user_path   query     string  false  "Filter by tracked user path subtree"
// @Param        cache_mode  query     string  false  "Cache mode filter: uncached, cached, all (default uncached)"
// @Success      200  {object}  usage.UsageSummary
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Router       /admin/api/v1/usage/summary [get]
func (h *Handler) UsageSummary(c *echo.Context) error {
	if h.usageReader == nil {
		return c.JSON(http.StatusOK, usage.UsageSummary{})
	}

	params, err := parseUsageParams(c)
	if err != nil {
		return handleError(c, err)
	}

	summary, err := h.usageReader.GetSummary(c.Request().Context(), params)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, summary)
}

func usageSliceResponse[T any](
	c *echo.Context,
	reader usage.UsageReader,
	fetch func(context.Context, usage.UsageQueryParams) ([]T, error),
) error {
	if reader == nil {
		return c.JSON(http.StatusOK, []T{})
	}

	params, err := parseUsageParams(c)
	if err != nil {
		return handleError(c, err)
	}

	values, err := fetch(c.Request().Context(), params)
	if err != nil {
		return handleError(c, err)
	}
	if values == nil {
		values = []T{}
	}
	return c.JSON(http.StatusOK, values)
}

// DailyUsage handles GET /admin/api/v1/usage/daily
//
// @Summary      Get usage breakdown by period
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        days        query     int     false  "Number of days (default 30)"
// @Param        start_date  query     string  false  "Start date (YYYY-MM-DD)"
// @Param        end_date    query     string  false  "End date (YYYY-MM-DD)"
// @Param        interval    query     string  false  "Grouping interval: daily, weekly, monthly, yearly (default daily)"
// @Param        user_path   query     string  false  "Filter by tracked user path subtree"
// @Param        cache_mode  query     string  false  "Cache mode filter: uncached, cached, all (default uncached)"
// @Success      200  {array}   usage.DailyUsage
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Router       /admin/api/v1/usage/daily [get]
func (h *Handler) DailyUsage(c *echo.Context) error {
	return usageSliceResponse(c, h.usageReader, func(ctx context.Context, params usage.UsageQueryParams) ([]usage.DailyUsage, error) {
		return h.usageReader.GetDailyUsage(ctx, params)
	})
}

// UsageByModel handles GET /admin/api/v1/usage/models
//
// @Summary      Get usage breakdown by model
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        days        query     int     false  "Number of days (default 30)"
// @Param        start_date  query     string  false  "Start date (YYYY-MM-DD)"
// @Param        end_date    query     string  false  "End date (YYYY-MM-DD)"
// @Param        user_path   query     string  false  "Filter by tracked user path subtree"
// @Param        cache_mode  query     string  false  "Cache mode filter: uncached, cached, all (default uncached)"
// @Success      200  {array}   usage.ModelUsage
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Router       /admin/api/v1/usage/models [get]
func (h *Handler) UsageByModel(c *echo.Context) error {
	return usageSliceResponse(c, h.usageReader, func(ctx context.Context, params usage.UsageQueryParams) ([]usage.ModelUsage, error) {
		return h.usageReader.GetUsageByModel(ctx, params)
	})
}

// UsageByUserPath handles GET /admin/api/v1/usage/user-paths
//
// @Summary      Get usage breakdown by user path
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        days        query     int     false  "Number of days (default 30)"
// @Param        start_date  query     string  false  "Start date (YYYY-MM-DD)"
// @Param        end_date    query     string  false  "End date (YYYY-MM-DD)"
// @Param        user_path   query     string  false  "Filter by tracked user path subtree"
// @Param        cache_mode  query     string  false  "Cache mode filter: uncached, cached, all (default uncached)"
// @Success      200  {array}   usage.UserPathUsage
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Router       /admin/api/v1/usage/user-paths [get]
func (h *Handler) UsageByUserPath(c *echo.Context) error {
	return usageSliceResponse(c, h.usageReader, func(ctx context.Context, params usage.UsageQueryParams) ([]usage.UserPathUsage, error) {
		return h.usageReader.GetUsageByUserPath(ctx, params)
	})
}

// UsageLog handles GET /admin/api/v1/usage/log
//
// @Summary      Get paginated usage log entries
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        days        query     int     false  "Number of days (default 30)"
// @Param        start_date  query     string  false  "Start date (YYYY-MM-DD)"
// @Param        end_date    query     string  false  "End date (YYYY-MM-DD)"
// @Param        model       query     string  false  "Filter by model name"
// @Param        provider    query     string  false  "Filter by provider name or provider type"
// @Param        user_path   query     string  false  "Filter by tracked user path subtree"
// @Param        cache_mode  query     string  false  "Cache mode filter: uncached, cached, all (default uncached)"
// @Param        search      query     string  false  "Search across model, provider, request_id, provider_id"
// @Param        limit       query     int     false  "Page size (default 50, max 200)"
// @Param        offset      query     int     false  "Offset for pagination"
// @Success      200  {object}  usage.UsageLogResult
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Router       /admin/api/v1/usage/log [get]
func (h *Handler) UsageLog(c *echo.Context) error {
	if h.usageReader == nil {
		return c.JSON(http.StatusOK, usage.UsageLogResult{
			Entries: []usage.UsageLogEntry{},
		})
	}

	baseParams, err := parseUsageParams(c)
	if err != nil {
		return handleError(c, err)
	}

	params := usage.UsageLogParams{
		UsageQueryParams: baseParams,
		Model:            c.QueryParam("model"),
		Provider:         c.QueryParam("provider"),
		Search:           c.QueryParam("search"),
	}

	if l := c.QueryParam("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			params.Limit = parsed
		}
	}
	if o := c.QueryParam("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			params.Offset = parsed
		}
	}

	result, err := h.usageReader.GetUsageLog(c.Request().Context(), params)
	if err != nil {
		return handleError(c, err)
	}

	if result.Entries == nil {
		result.Entries = []usage.UsageLogEntry{}
	}

	return c.JSON(http.StatusOK, result)
}

// CacheOverview handles GET /admin/api/v1/cache/overview
//
// @Summary      Get cached-only usage overview
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        days        query     int     false  "Number of days (default 30)"
// @Param        start_date  query     string  false  "Start date (YYYY-MM-DD)"
// @Param        end_date    query     string  false  "End date (YYYY-MM-DD)"
// @Param        interval    query     string  false  "Grouping interval: daily, weekly, monthly, yearly (default daily)"
// @Param        user_path   query     string  false  "Filter by tracked user path subtree"
// @Param        cache_mode  query     string  false  "Cache mode filter: uncached, cached, all (cache overview always uses cached mode)"
// @Success      200  {object}  usage.CacheOverview
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Failure      503  {object}  core.GatewayError
// @Router       /admin/api/v1/cache/overview [get]
func (h *Handler) CacheOverview(c *echo.Context) error {
	if strings.TrimSpace(h.runtimeConfig.CacheEnabled) != "on" {
		return handleError(c, featureUnavailableError("cache analytics is unavailable"))
	}
	if h.usageReader == nil {
		return c.JSON(http.StatusOK, usage.CacheOverview{
			Daily: []usage.CacheOverviewDaily{},
		})
	}

	params, err := parseUsageParams(c)
	if err != nil {
		return handleError(c, err)
	}
	params.CacheMode = usage.CacheModeCached

	overview, err := h.usageReader.GetCacheOverview(c.Request().Context(), params)
	if err != nil {
		return handleError(c, err)
	}
	if overview == nil {
		overview = &usage.CacheOverview{}
	}
	if overview.Daily == nil {
		overview.Daily = []usage.CacheOverviewDaily{}
	}

	return c.JSON(http.StatusOK, overview)
}

// AuditLog handles GET /admin/api/v1/audit/log
//
// @Summary      Get paginated audit log entries
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        days         query     int     false  "Number of days (default 30)"
// @Param        start_date   query     string  false  "Start date (YYYY-MM-DD)"
// @Param        end_date     query     string  false  "End date (YYYY-MM-DD)"
// @Param        requested_model  query     string  false  "Filter by requested model selector"
// @Param        provider     query     string  false  "Filter by provider name or provider type"
// @Param        method       query     string  false  "Filter by HTTP method"
// @Param        path         query     string  false  "Filter by request path"
// @Param        user_path    query     string  false  "Filter by tracked user path subtree"
// @Param        error_type   query     string  false  "Filter by error type"
// @Param        status_code  query     int     false  "Filter by status code"
// @Param        stream       query     bool    false  "Filter by stream mode (true/false)"
// @Param        search       query     string  false  "Search across request_id/requested_model/provider/method/path/error_type/error_message"
// @Param        limit        query     int     false  "Page size (default 25, max 100)"
// @Param        offset       query     int     false  "Offset for pagination"
// @Success      200  {object}  auditlog.LogListResult
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Router       /admin/api/v1/audit/log [get]
func (h *Handler) AuditLog(c *echo.Context) error {
	if h.auditReader == nil {
		return c.JSON(http.StatusOK, auditlog.LogListResult{
			Entries: []auditlog.LogEntry{},
		})
	}

	dateRange, err := parseDateRangeParams(c)
	if err != nil {
		return handleError(c, err)
	}
	userPath, err := normalizeUserPathQueryParam("user_path", c.QueryParam("user_path"))
	if err != nil {
		return handleError(c, err)
	}

	requestedModel := c.QueryParam("requested_model")
	if requestedModel == "" {
		requestedModel = c.QueryParam("model")
	}

	params := auditlog.LogQueryParams{
		QueryParams: auditlog.QueryParams{
			StartDate: dateRange.StartDate,
			EndDate:   dateRange.EndDate,
		},
		RequestedModel: requestedModel,
		Provider:       c.QueryParam("provider"),
		Method:         strings.ToUpper(c.QueryParam("method")),
		Path:           c.QueryParam("path"),
		UserPath:       userPath,
		ErrorType:      c.QueryParam("error_type"),
		Search:         c.QueryParam("search"),
	}

	if sc := c.QueryParam("status_code"); sc != "" {
		parsed, err := strconv.Atoi(sc)
		if err != nil {
			return handleError(c, core.NewInvalidRequestError("invalid status_code, expected integer", nil))
		}
		params.StatusCode = &parsed
	}

	if stream := c.QueryParam("stream"); stream != "" {
		parsed, err := strconv.ParseBool(stream)
		if err != nil {
			return handleError(c, core.NewInvalidRequestError("invalid stream value, expected true or false", nil))
		}
		params.Stream = &parsed
	}

	if l := c.QueryParam("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			params.Limit = parsed
		}
	}
	if o := c.QueryParam("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			params.Offset = parsed
		}
	}

	result, err := h.auditReader.GetLogs(c.Request().Context(), params)
	if err != nil {
		return handleError(c, err)
	}

	if result.Entries == nil {
		result.Entries = []auditlog.LogEntry{}
	}

	return c.JSON(http.StatusOK, result)
}

// AuditConversation handles GET /admin/api/v1/audit/conversation
//
// @Summary      Get conversation thread around an audit log entry
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        log_id  query     string  true   "Anchor audit log entry ID"
// @Param        limit   query     int     false  "Max entries in thread (default 40, max 200)"
// @Success      200  {object}  auditlog.ConversationResult
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Router       /admin/api/v1/audit/conversation [get]
func (h *Handler) AuditConversation(c *echo.Context) error {
	if h.auditReader == nil {
		return c.JSON(http.StatusOK, auditlog.ConversationResult{
			AnchorID: c.QueryParam("log_id"),
			Entries:  []auditlog.LogEntry{},
		})
	}

	logID := strings.TrimSpace(c.QueryParam("log_id"))
	if logID == "" {
		return handleError(c, core.NewInvalidRequestError("log_id is required", nil))
	}

	limit := 40
	if l := c.QueryParam("limit"); l != "" {
		parsed, err := strconv.Atoi(l)
		if err != nil {
			return handleError(c, core.NewInvalidRequestError("invalid limit, expected integer", nil))
		}
		if parsed < 1 || parsed > 200 {
			return handleError(c, core.NewInvalidRequestError("invalid limit parameter: limit must be between 1 and 200", nil))
		}
		limit = parsed
	}

	result, err := h.auditReader.GetConversation(c.Request().Context(), logID, limit)
	if err != nil {
		return handleError(c, err)
	}
	if result == nil {
		result = &auditlog.ConversationResult{
			AnchorID: logID,
			Entries:  []auditlog.LogEntry{},
		}
	}
	if result.Entries == nil {
		result.Entries = []auditlog.LogEntry{}
	}

	return c.JSON(http.StatusOK, result)
}

// ListModels handles GET /admin/api/v1/models
// Supports optional ?category= query param for filtering by model category.
//
// @Summary      List all registered models with provider info
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Success      200  {array}  providers.ModelWithProvider
// @Failure      401  {object}  core.GatewayError
// @Router       /admin/api/v1/models [get]
type modelAccessResponse struct {
	Selector         string                   `json:"selector"`
	DefaultEnabled   bool                     `json:"default_enabled"`
	EffectiveEnabled bool                     `json:"effective_enabled"`
	UserPaths        []string                 `json:"user_paths,omitempty"`
	Override         *modeloverrides.Override `json:"override,omitempty"`
}

type modelInventoryResponse struct {
	providers.ModelWithProvider
	Access modelAccessResponse `json:"access"`
}

func (h *Handler) ListModels(c *echo.Context) error {
	if h.registry == nil {
		return c.JSON(http.StatusOK, []modelInventoryResponse{})
	}

	cat := core.ModelCategory(c.QueryParam("category"))
	if cat != "" && cat != core.CategoryAll {
		if !isValidCategory(cat) {
			return handleError(c, core.NewInvalidRequestError("invalid category: "+string(cat), nil))
		}
	}

	var models []providers.ModelWithProvider
	if cat != "" && cat != core.CategoryAll {
		models = h.registry.ListModelsWithProviderByCategory(cat)
	} else {
		models = h.registry.ListModelsWithProvider()
	}

	if models == nil {
		models = []providers.ModelWithProvider{}
	}
	if h.modelOverrides == nil {
		response := make([]modelInventoryResponse, 0, len(models))
		for _, model := range models {
			selector := core.ModelSelector{
				Provider: strings.TrimSpace(model.ProviderName),
				Model:    strings.TrimSpace(model.Model.ID),
			}
			response = append(response, modelInventoryResponse{
				ModelWithProvider: model,
				Access: modelAccessResponse{
					Selector:         selector.QualifiedModel(),
					DefaultEnabled:   true,
					EffectiveEnabled: true,
				},
			})
		}
		return c.JSON(http.StatusOK, response)
	}

	response := make([]modelInventoryResponse, 0, len(models))
	for _, model := range models {
		selector := core.ModelSelector{
			Provider: strings.TrimSpace(model.ProviderName),
			Model:    strings.TrimSpace(model.Model.ID),
		}
		effective := h.modelOverrides.EffectiveState(selector)
		access := modelAccessResponse{
			Selector:         effective.Selector,
			DefaultEnabled:   effective.DefaultEnabled,
			EffectiveEnabled: effective.Enabled,
			UserPaths:        append([]string(nil), effective.UserPaths...),
		}
		if override, ok := h.modelOverrides.Get(selector.QualifiedModel()); ok && override != nil {
			overrideCopy := *override
			access.Override = &overrideCopy
		}
		response = append(response, modelInventoryResponse{
			ModelWithProvider: model,
			Access:            access,
		})
	}

	return c.JSON(http.StatusOK, response)
}

// isValidCategory returns true if cat is a recognized model category.
func isValidCategory(cat core.ModelCategory) bool {
	return slices.Contains(core.AllCategories(), cat)
}

// ListCategories handles GET /admin/api/v1/models/categories
//
// @Summary      List model categories with counts
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Success      200  {array}   providers.CategoryCount
// @Failure      401  {object}  core.GatewayError
// @Router       /admin/api/v1/models/categories [get]
func (h *Handler) ListCategories(c *echo.Context) error {
	if h.registry == nil {
		return c.JSON(http.StatusOK, []providers.CategoryCount{})
	}

	return c.JSON(http.StatusOK, h.registry.GetCategoryCounts())
}

// DashboardConfig handles GET /admin/api/v1/dashboard/config
func (h *Handler) DashboardConfig(c *echo.Context) error {
	return c.JSON(http.StatusOK, cloneDashboardRuntimeConfig(h.runtimeConfig))
}

// ProviderStatus handles GET /admin/api/v1/providers/status
func (h *Handler) ProviderStatus(c *echo.Context) error {
	return c.JSON(http.StatusOK, h.buildProviderStatusResponse())
}

// RefreshRuntime handles POST /admin/api/v1/runtime/refresh
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

	resp := providerStatusResponse{
		Summary: providerStatusSummaryResponse{
			OverallStatus: "degraded",
		},
		Providers: make([]providerStatusItemResponse, 0, len(names)),
	}

	for _, name := range names {
		cfg, hasConfig := configuredByName[name]
		runtime, hasRuntime := runtimeByName[name]
		if !hasConfig {
			cfg = providers.SanitizedProviderConfig{Name: name, Type: strings.TrimSpace(runtime.Type)}
		}
		if !hasRuntime {
			runtime = providers.ProviderRuntimeSnapshot{Name: name, Type: strings.TrimSpace(cfg.Type)}
		}
		if strings.TrimSpace(cfg.Type) == "" {
			cfg.Type = strings.TrimSpace(runtime.Type)
		}
		if strings.TrimSpace(runtime.Type) == "" {
			runtime.Type = strings.TrimSpace(cfg.Type)
		}

		status, label, reason, lastError := classifyProviderStatus(cfg, runtime)
		resp.Providers = append(resp.Providers, providerStatusItemResponse{
			Name:         name,
			Type:         strings.TrimSpace(cfg.Type),
			Status:       status,
			StatusLabel:  label,
			StatusReason: reason,
			LastError:    lastError,
			Config:       cfg,
			Runtime:      runtime,
		})
		resp.Summary.Total++
		switch status {
		case "healthy":
			resp.Summary.Healthy++
		case "unhealthy":
			resp.Summary.Unhealthy++
		default:
			resp.Summary.Degraded++
		}
	}

	switch {
	case resp.Summary.Total == 0:
		resp.Summary.OverallStatus = "degraded"
	case resp.Summary.Healthy == resp.Summary.Total:
		resp.Summary.OverallStatus = "healthy"
	case resp.Summary.Unhealthy == resp.Summary.Total:
		resp.Summary.OverallStatus = "unhealthy"
	default:
		resp.Summary.OverallStatus = "degraded"
	}

	if resp.Providers == nil {
		resp.Providers = []providerStatusItemResponse{}
	}
	return resp
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

type upsertAliasRequest struct {
	TargetModel    string `json:"target_model"`
	TargetProvider string `json:"target_provider,omitempty"`
	Description    string `json:"description,omitempty"`
	Enabled        *bool  `json:"enabled,omitempty"`
}

type upsertModelOverrideRequest struct {
	UserPaths []string `json:"user_paths,omitempty"`
}

type upsertGuardrailRequest struct {
	Type        string          `json:"type"`
	Description string          `json:"description,omitempty"`
	UserPath    string          `json:"user_path,omitempty"`
	Config      json.RawMessage `json:"config"`
}

type createWorkflowRequest struct {
	ScopeProviderName   string            `json:"scope_provider_name,omitempty"`
	LegacyScopeProvider string            `json:"scope_provider,omitempty"`
	ScopeModel          string            `json:"scope_model,omitempty"`
	ScopeUserPath       string            `json:"scope_user_path,omitempty"`
	Name                string            `json:"name"`
	Description         string            `json:"description,omitempty"`
	Payload             workflows.Payload `json:"workflow_payload"`
}

type createAuthKeyRequest struct {
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	UserPath    string     `json:"user_path,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

func featureUnavailableError(message string) error {
	return core.NewInvalidRequestErrorWithStatus(http.StatusServiceUnavailable, message, nil).
		WithCode("feature_unavailable")
}

func (h *Handler) aliasesUnavailableError() error {
	return featureUnavailableError("aliases feature is unavailable")
}

func (h *Handler) modelOverridesUnavailableError() error {
	return featureUnavailableError("model overrides feature is unavailable")
}

func (h *Handler) authKeysUnavailableError() error {
	return featureUnavailableError("auth keys feature is unavailable")
}

func (h *Handler) guardrailsUnavailableError() error {
	return featureUnavailableError("guardrails feature is unavailable")
}

func (h *Handler) workflowsUnavailableError() error {
	return featureUnavailableError("workflows feature is unavailable")
}

func aliasWriteError(err error) error {
	if err == nil {
		return nil
	}
	if aliases.IsValidationError(err) {
		return core.NewInvalidRequestError(err.Error(), err)
	}
	return err
}

func modelOverrideWriteError(err error) error {
	if err == nil {
		return nil
	}
	if modeloverrides.IsValidationError(err) {
		return core.NewInvalidRequestError(err.Error(), err)
	}
	return core.NewProviderError("model_overrides", http.StatusBadGateway, err.Error(), err)
}

func workflowWriteError(err error) error {
	if err == nil {
		return nil
	}
	if workflows.IsValidationError(err) {
		return core.NewInvalidRequestError(err.Error(), err)
	}
	return err
}

func authKeyWriteError(err error) error {
	if err == nil {
		return nil
	}
	if authkeys.IsValidationError(err) {
		return core.NewInvalidRequestError(err.Error(), err)
	}
	return err
}

func guardrailWriteError(err error) error {
	if err == nil {
		return nil
	}
	if guardrails.IsValidationError(err) {
		return core.NewInvalidRequestError(err.Error(), err)
	}
	return err
}

func deactivateByID(
	c *echo.Context,
	unavailableErr error,
	idLabel string,
	notFoundErr error,
	notFoundMessage string,
	deactivate func(context.Context, string) error,
	writeError func(error) error,
) error {
	if unavailableErr != nil {
		return handleError(c, unavailableErr)
	}

	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return handleError(c, core.NewInvalidRequestError(idLabel+" id is required", nil))
	}

	if err := deactivate(c.Request().Context(), id); err != nil {
		if errors.Is(err, notFoundErr) {
			return handleError(c, core.NewNotFoundError(notFoundMessage+id))
		}
		return handleError(c, writeError(err))
	}
	return c.NoContent(http.StatusNoContent)
}

func deleteByName(
	c *echo.Context,
	unavailableErr error,
	paramName string,
	decode func(string) (string, error),
	deleteFunc func(context.Context, string) error,
	notFoundErr error,
	notFoundMessage string,
	writeError func(error) error,
) error {
	if unavailableErr != nil {
		return handleError(c, unavailableErr)
	}

	name, err := decode(c.Param(paramName))
	if err != nil {
		return handleError(c, err)
	}

	if err := deleteFunc(c.Request().Context(), name); err != nil {
		if errors.Is(err, notFoundErr) {
			return handleError(c, core.NewNotFoundError(notFoundMessage+name))
		}
		return handleError(c, writeError(err))
	}
	return c.NoContent(http.StatusNoContent)
}

// ListModelOverrides handles GET /admin/api/v1/model-overrides.
func (h *Handler) ListModelOverrides(c *echo.Context) error {
	if h.modelOverrides == nil {
		return handleError(c, h.modelOverridesUnavailableError())
	}
	views := h.modelOverrides.ListViews()
	if views == nil {
		views = []modeloverrides.View{}
	}
	return c.JSON(http.StatusOK, views)
}

// UpsertModelOverride handles PUT /admin/api/v1/model-overrides/{selector}.
func (h *Handler) UpsertModelOverride(c *echo.Context) error {
	if h.modelOverrides == nil {
		return handleError(c, h.modelOverridesUnavailableError())
	}

	selector, err := decodeModelOverridePathSelector(c.Param("selector"))
	if err != nil {
		return handleError(c, err)
	}

	var req upsertModelOverrideRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}

	if err := h.modelOverrides.Upsert(c.Request().Context(), modeloverrides.Override{
		Selector:  selector,
		UserPaths: req.UserPaths,
	}); err != nil {
		return handleError(c, modelOverrideWriteError(err))
	}

	override, ok := h.modelOverrides.Get(selector)
	if !ok || override == nil {
		slog.Error("model override service returned no override after upsert", "selector", selector)
		return handleError(c, core.NewProviderError("model_overrides", http.StatusInternalServerError, "model override update failed unexpectedly", nil))
	}
	return c.JSON(http.StatusOK, override)
}

// DeleteModelOverride handles DELETE /admin/api/v1/model-overrides/{selector}.
func (h *Handler) DeleteModelOverride(c *echo.Context) error {
	var unavailableErr error
	var deleteFunc func(context.Context, string) error
	if h.modelOverrides == nil {
		unavailableErr = h.modelOverridesUnavailableError()
	} else {
		deleteFunc = h.modelOverrides.Delete
	}
	return deleteByName(
		c,
		unavailableErr,
		"selector",
		decodeModelOverridePathSelector,
		deleteFunc,
		modeloverrides.ErrNotFound,
		"model override not found: ",
		modelOverrideWriteError,
	)
}

// ListAuthKeys handles GET /admin/api/v1/auth-keys
func (h *Handler) ListAuthKeys(c *echo.Context) error {
	if h.authKeys == nil {
		return handleError(c, h.authKeysUnavailableError())
	}
	views := h.authKeys.ListViews()
	if views == nil {
		views = []authkeys.View{}
	}
	return c.JSON(http.StatusOK, views)
}

// CreateAuthKey handles POST /admin/api/v1/auth-keys
func (h *Handler) CreateAuthKey(c *echo.Context) error {
	if h.authKeys == nil {
		return handleError(c, h.authKeysUnavailableError())
	}

	var req createAuthKeyRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}

	userPath, err := normalizeUserPathQueryParam("user_path", req.UserPath)
	if err != nil {
		return handleError(c, err)
	}

	issued, err := h.authKeys.Create(c.Request().Context(), authkeys.CreateInput{
		Name:        req.Name,
		Description: req.Description,
		UserPath:    userPath,
		ExpiresAt:   req.ExpiresAt,
	})
	if err != nil {
		return handleError(c, authKeyWriteError(err))
	}
	if issued == nil {
		requestID := strings.TrimSpace(core.GetRequestID(c.Request().Context()))
		slog.Error("auth key service returned nil issued key", "request_id", requestID, "path", c.Request().URL.Path)
		return c.JSON(http.StatusInternalServerError, (&core.GatewayError{
			Type:       core.ErrorType("internal_error"),
			Message:    "auth key creation failed unexpectedly",
			StatusCode: http.StatusInternalServerError,
		}).WithCode("auth_key_issue_failed").ToJSON())
	}
	return c.JSON(http.StatusCreated, issued)
}

// DeactivateAuthKey handles POST /admin/api/v1/auth-keys/:id/deactivate
func (h *Handler) DeactivateAuthKey(c *echo.Context) error {
	var unavailableErr error
	var deactivate func(context.Context, string) error
	if h.authKeys == nil {
		unavailableErr = h.authKeysUnavailableError()
	} else {
		deactivate = h.authKeys.Deactivate
	}
	return deactivateByID(c, unavailableErr, "auth key", authkeys.ErrNotFound, "auth key not found: ", deactivate, authKeyWriteError)
}

// ListAliases handles GET /admin/api/v1/aliases
func (h *Handler) ListAliases(c *echo.Context) error {
	if h.aliases == nil {
		return handleError(c, h.aliasesUnavailableError())
	}
	views := h.aliases.ListViews()
	if views == nil {
		views = []aliases.View{}
	}
	return c.JSON(http.StatusOK, views)
}

// UpsertAlias handles PUT /admin/api/v1/aliases/{name}
func (h *Handler) UpsertAlias(c *echo.Context) error {
	if h.aliases == nil {
		return handleError(c, h.aliasesUnavailableError())
	}

	name, err := decodeAliasPathName(c.Param("name"))
	if err != nil {
		return handleError(c, err)
	}

	var req upsertAliasRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}

	enabled := true
	if existing, ok := h.aliases.Get(name); ok && existing != nil {
		enabled = existing.Enabled
	}
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	if err := h.aliases.Upsert(c.Request().Context(), aliases.Alias{
		Name:           name,
		TargetModel:    req.TargetModel,
		TargetProvider: req.TargetProvider,
		Description:    req.Description,
		Enabled:        enabled,
	}); err != nil {
		return handleError(c, aliasWriteError(err))
	}

	alias, ok := h.aliases.Get(name)
	if !ok {
		return c.NoContent(http.StatusNoContent)
	}
	return c.JSON(http.StatusOK, alias)
}

// DeleteAlias handles DELETE /admin/api/v1/aliases/{name}
func (h *Handler) DeleteAlias(c *echo.Context) error {
	var unavailableErr error
	var deleteFunc func(context.Context, string) error
	if h.aliases == nil {
		unavailableErr = h.aliasesUnavailableError()
	} else {
		deleteFunc = h.aliases.Delete
	}
	return deleteByName(
		c,
		unavailableErr,
		"name",
		decodeAliasPathName,
		deleteFunc,
		aliases.ErrNotFound,
		"alias not found: ",
		aliasWriteError,
	)
}

// ListGuardrailTypes handles GET /admin/api/v1/guardrails/types
func (h *Handler) ListGuardrailTypes(c *echo.Context) error {
	if h.guardrailDefs == nil {
		return handleError(c, h.guardrailsUnavailableError())
	}
	return c.JSON(http.StatusOK, h.guardrailDefs.TypeDefinitions())
}

// ListGuardrails handles GET /admin/api/v1/guardrails
func (h *Handler) ListGuardrails(c *echo.Context) error {
	if h.guardrailDefs == nil {
		return handleError(c, h.guardrailsUnavailableError())
	}
	views := h.guardrailDefs.ListViews()
	if views == nil {
		views = []guardrails.View{}
	}
	return c.JSON(http.StatusOK, views)
}

// UpsertGuardrail handles PUT /admin/api/v1/guardrails/{name}
func (h *Handler) UpsertGuardrail(c *echo.Context) error {
	if h.guardrailDefs == nil {
		return handleError(c, h.guardrailsUnavailableError())
	}

	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		return handleError(c, core.NewInvalidRequestError("guardrail name is required", nil))
	}

	var req upsertGuardrailRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}

	userPath, err := normalizeUserPathQueryParam("user_path", req.UserPath)
	if err != nil {
		return handleError(c, err)
	}

	h.mutationMu.Lock()
	defer h.mutationMu.Unlock()

	if err := h.guardrailDefs.Upsert(c.Request().Context(), guardrails.Definition{
		Name:        name,
		Type:        req.Type,
		Description: req.Description,
		UserPath:    userPath,
		Config:      req.Config,
	}); err != nil {
		return handleError(c, guardrailWriteError(err))
	}
	if err := h.refreshWorkflowsAfterGuardrailChange(c.Request().Context()); err != nil {
		return handleError(c, err)
	}

	definition, ok := h.guardrailDefs.Get(name)
	if !ok {
		return c.NoContent(http.StatusNoContent)
	}
	return c.JSON(http.StatusOK, guardrails.ViewFromDefinition(*definition))
}

// DeleteGuardrail handles DELETE /admin/api/v1/guardrails/{name}
func (h *Handler) DeleteGuardrail(c *echo.Context) error {
	if h.guardrailDefs == nil {
		return handleError(c, h.guardrailsUnavailableError())
	}

	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		return handleError(c, core.NewInvalidRequestError("guardrail name is required", nil))
	}

	h.mutationMu.Lock()
	defer h.mutationMu.Unlock()

	referencingWorkflows, err := h.activeWorkflowGuardrailReferences(c.Request().Context(), name)
	if err != nil {
		return handleError(c, err)
	}
	if len(referencingWorkflows) > 0 {
		return handleError(c, core.NewInvalidRequestError("guardrail is used by active workflows: "+strings.Join(referencingWorkflows, ", "), nil))
	}

	if err := h.guardrailDefs.Delete(c.Request().Context(), name); err != nil {
		if errors.Is(err, guardrails.ErrNotFound) {
			return handleError(c, core.NewNotFoundError("guardrail not found: "+name))
		}
		return handleError(c, guardrailWriteError(err))
	}
	if err := h.refreshWorkflowsAfterGuardrailChange(c.Request().Context()); err != nil {
		return handleError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
}

// ListWorkflows handles GET /admin/api/v1/workflows
func (h *Handler) ListWorkflows(c *echo.Context) error {
	if h.workflows == nil {
		return handleError(c, h.workflowsUnavailableError())
	}

	views, err := h.workflows.ListViews(c.Request().Context())
	if err != nil {
		return handleError(c, err)
	}
	if views == nil {
		views = []workflows.View{}
	}
	return c.JSON(http.StatusOK, views)
}

// GetWorkflow handles GET /admin/api/v1/workflows/:id
func (h *Handler) GetWorkflow(c *echo.Context) error {
	if h.workflows == nil {
		return handleError(c, h.workflowsUnavailableError())
	}

	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return handleError(c, core.NewInvalidRequestError("workflow id is required", nil))
	}

	view, err := h.workflows.GetView(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, workflows.ErrNotFound) {
			return handleError(c, core.NewNotFoundError("workflow not found: "+id))
		}
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, view)
}

// ListWorkflowGuardrails handles GET /admin/api/v1/workflows/guardrails
func (h *Handler) ListWorkflowGuardrails(c *echo.Context) error {
	if h.guardrails == nil {
		return c.JSON(http.StatusOK, []string{})
	}

	return c.JSON(http.StatusOK, h.guardrails.Names())
}

// CreateWorkflow handles POST /admin/api/v1/workflows
func (h *Handler) CreateWorkflow(c *echo.Context) error {
	if h.workflows == nil {
		return handleError(c, h.workflowsUnavailableError())
	}

	var req createWorkflowRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	scopeProviderName := strings.TrimSpace(req.ScopeProviderName)
	if scopeProviderName == "" {
		scopeProviderName = strings.TrimSpace(req.LegacyScopeProvider)
	}
	scopeModel := strings.TrimSpace(req.ScopeModel)

	scopeUserPath, err := normalizeUserPathQueryParam("scope_user_path", req.ScopeUserPath)
	if err != nil {
		return handleError(c, err)
	}

	scopeProviderName, err = h.validateWorkflowScope(scopeProviderName, scopeModel)
	if err != nil {
		return handleError(c, err)
	}

	if err := h.validateWorkflowGuardrails(req.Payload); err != nil {
		return handleError(c, err)
	}

	h.mutationMu.Lock()
	defer h.mutationMu.Unlock()

	version, err := h.workflows.Create(c.Request().Context(), workflows.CreateInput{
		Scope: workflows.Scope{
			Provider: scopeProviderName,
			Model:    scopeModel,
			UserPath: scopeUserPath,
		},
		Activate:    true,
		Name:        req.Name,
		Description: req.Description,
		Payload:     req.Payload,
	})
	if err != nil {
		return handleError(c, workflowWriteError(err))
	}
	if version == nil {
		return c.NoContent(http.StatusNoContent)
	}
	return c.JSON(http.StatusCreated, version)
}

// DeactivateWorkflow handles POST /admin/api/v1/workflows/:id/deactivate
func (h *Handler) DeactivateWorkflow(c *echo.Context) error {
	if h.workflows == nil {
		return handleError(c, h.workflowsUnavailableError())
	}

	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return handleError(c, core.NewInvalidRequestError("workflow id is required", nil))
	}

	h.mutationMu.Lock()
	defer h.mutationMu.Unlock()

	if err := h.workflows.Deactivate(c.Request().Context(), id); err != nil {
		if errors.Is(err, workflows.ErrNotFound) {
			return handleError(c, core.NewNotFoundError("workflow not found: "+id))
		}
		return handleError(c, workflowWriteError(err))
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *Handler) refreshWorkflowsAfterGuardrailChange(ctx context.Context) error {
	if h.workflows == nil {
		return nil
	}
	if err := h.workflows.Refresh(ctx); err != nil {
		return err
	}
	return nil
}

func (h *Handler) activeWorkflowGuardrailReferences(ctx context.Context, name string) ([]string, error) {
	if h.workflows == nil {
		return nil, nil
	}

	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}

	views, err := h.workflows.ListViews(ctx)
	if err != nil {
		return nil, err
	}

	references := make([]string, 0)
	for _, view := range views {
		if !view.Payload.Features.Guardrails {
			continue
		}
		for _, step := range view.Payload.Guardrails {
			if strings.TrimSpace(step.Ref) != name {
				continue
			}
			references = append(references, view.ScopeDisplay)
			break
		}
	}
	sort.Strings(references)
	return references, nil
}

func (h *Handler) validateWorkflowGuardrails(payload workflows.Payload) error {
	if !payload.Features.Guardrails || len(payload.Guardrails) == 0 {
		return nil
	}
	if h.guardrails == nil {
		return featureUnavailableError("guardrail registry is unavailable for workflow authoring")
	}

	known := make(map[string]struct{}, h.guardrails.Len())
	for _, name := range h.guardrails.Names() {
		known[name] = struct{}{}
	}
	for _, step := range payload.Guardrails {
		ref := strings.TrimSpace(step.Ref)
		if ref == "" {
			continue
		}
		if _, ok := known[ref]; !ok {
			return core.NewInvalidRequestError("unknown guardrail ref: "+ref, nil)
		}
	}
	return nil
}

func (h *Handler) validateWorkflowScope(scopeProviderName, scopeModel string) (string, error) {
	scopeProviderName = strings.TrimSpace(scopeProviderName)
	scopeModel = strings.TrimSpace(scopeModel)

	if scopeProviderName == "" {
		if scopeModel != "" {
			return "", core.NewInvalidRequestError("scope_model requires scope_provider_name", nil)
		}
		return "", nil
	}
	if h.registry == nil {
		return "", core.NewInvalidRequestError("provider registry is unavailable for workflow provider-name validation", nil)
	}
	if !slices.Contains(h.registry.ProviderNames(), scopeProviderName) {
		if resolvedProviderName := strings.TrimSpace(h.registry.GetProviderNameForType(scopeProviderName)); resolvedProviderName != "" {
			scopeProviderName = resolvedProviderName
		}
	}
	if !slices.Contains(h.registry.ProviderNames(), scopeProviderName) {
		return "", core.NewInvalidRequestError("unknown provider name: "+scopeProviderName, nil)
	}
	if scopeModel == "" {
		return scopeProviderName, nil
	}

	for _, model := range h.registry.ListModelsWithProvider() {
		if model.ProviderName == scopeProviderName && model.Model.ID == scopeModel {
			return scopeProviderName, nil
		}
	}
	return "", core.NewInvalidRequestError("unknown model for provider name "+scopeProviderName+": "+scopeModel, nil)
}

func decodeAliasPathName(raw string) (string, error) {
	name, err := url.PathUnescape(strings.TrimSpace(raw))
	if err != nil {
		return "", core.NewInvalidRequestError("invalid alias name", err)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", core.NewInvalidRequestError("alias name is required", nil)
	}
	return name, nil
}

func decodeModelOverridePathSelector(raw string) (string, error) {
	selector, err := url.PathUnescape(strings.TrimSpace(raw))
	if err != nil {
		return "", core.NewInvalidRequestError("invalid model override selector", err)
	}
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return "", core.NewInvalidRequestError("model override selector is required", nil)
	}
	return selector, nil
}
