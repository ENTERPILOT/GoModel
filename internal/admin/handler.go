// Package admin provides the admin REST API and dashboard for GOModel.
package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"gomodel/internal/aliases"
	"gomodel/internal/auditlog"
	"gomodel/internal/authkeys"
	"gomodel/internal/core"
	"gomodel/internal/executionplans"
	"gomodel/internal/guardrails"
	"gomodel/internal/providers"
	"gomodel/internal/usage"
)

// Handler serves admin API endpoints.
type Handler struct {
	usageReader   usage.UsageReader
	auditReader   auditlog.Reader
	registry      *providers.ModelRegistry
	authKeys      *authkeys.Service
	aliases       *aliases.Service
	plans         *executionplans.Service
	guardrails    *guardrails.Registry
	runtimeConfig DashboardConfigResponse
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

// WithExecutionPlans enables execution-plan administration endpoints.
func WithExecutionPlans(service *executionplans.Service) Option {
	return func(h *Handler) {
		h.plans = service
	}
}

// WithGuardrailsRegistry enables listing valid guardrail references for plan authoring.
func WithGuardrailsRegistry(registry *guardrails.Registry) Option {
	return func(h *Handler) {
		h.guardrails = registry
	}
}

// WithDashboardRuntimeConfig enables the allowlisted dashboard runtime config endpoint.
func WithDashboardRuntimeConfig(values DashboardConfigResponse) Option {
	return func(h *Handler) {
		h.runtimeConfig = normalizeDashboardRuntimeConfig(values)
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
// @Param        provider    query     string  false  "Filter by provider"
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
// @Param        model        query     string  false  "Filter by model name"
// @Param        provider     query     string  false  "Filter by provider"
// @Param        method       query     string  false  "Filter by HTTP method"
// @Param        path         query     string  false  "Filter by request path"
// @Param        user_path    query     string  false  "Filter by tracked user path subtree"
// @Param        error_type   query     string  false  "Filter by error type"
// @Param        status_code  query     int     false  "Filter by status code"
// @Param        stream       query     bool    false  "Filter by stream mode (true/false)"
// @Param        search       query     string  false  "Search across request_id/model/provider/method/path/error_type"
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

	params := auditlog.LogQueryParams{
		QueryParams: auditlog.QueryParams{
			StartDate: dateRange.StartDate,
			EndDate:   dateRange.EndDate,
		},
		Model:     c.QueryParam("model"),
		Provider:  c.QueryParam("provider"),
		Method:    strings.ToUpper(c.QueryParam("method")),
		Path:      c.QueryParam("path"),
		UserPath:  userPath,
		ErrorType: c.QueryParam("error_type"),
		Search:    c.QueryParam("search"),
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
func (h *Handler) ListModels(c *echo.Context) error {
	if h.registry == nil {
		return c.JSON(http.StatusOK, []providers.ModelWithProvider{})
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

	return c.JSON(http.StatusOK, models)
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

type upsertAliasRequest struct {
	TargetModel    string `json:"target_model"`
	TargetProvider string `json:"target_provider,omitempty"`
	Description    string `json:"description,omitempty"`
	Enabled        *bool  `json:"enabled,omitempty"`
}

type createExecutionPlanRequest struct {
	ScopeProvider string                 `json:"scope_provider,omitempty"`
	ScopeModel    string                 `json:"scope_model,omitempty"`
	ScopeUserPath string                 `json:"scope_user_path,omitempty"`
	Name          string                 `json:"name"`
	Description   string                 `json:"description,omitempty"`
	Payload       executionplans.Payload `json:"plan_payload"`
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

func (h *Handler) authKeysUnavailableError() error {
	return featureUnavailableError("auth keys feature is unavailable")
}

func (h *Handler) executionPlansUnavailableError() error {
	return featureUnavailableError("execution plans feature is unavailable")
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

func executionPlanWriteError(err error) error {
	if err == nil {
		return nil
	}
	if executionplans.IsValidationError(err) {
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
	if h.aliases == nil {
		return handleError(c, h.aliasesUnavailableError())
	}

	name, err := decodeAliasPathName(c.Param("name"))
	if err != nil {
		return handleError(c, err)
	}

	if err := h.aliases.Delete(c.Request().Context(), name); err != nil {
		if errors.Is(err, aliases.ErrNotFound) {
			return handleError(c, core.NewNotFoundError("alias not found: "+name))
		}
		return handleError(c, aliasWriteError(err))
	}
	return c.NoContent(http.StatusNoContent)
}

// ListExecutionPlans handles GET /admin/api/v1/execution-plans
func (h *Handler) ListExecutionPlans(c *echo.Context) error {
	if h.plans == nil {
		return handleError(c, h.executionPlansUnavailableError())
	}

	views, err := h.plans.ListViews(c.Request().Context())
	if err != nil {
		return handleError(c, err)
	}
	if views == nil {
		views = []executionplans.View{}
	}
	return c.JSON(http.StatusOK, views)
}

// GetExecutionPlan handles GET /admin/api/v1/execution-plans/:id
func (h *Handler) GetExecutionPlan(c *echo.Context) error {
	if h.plans == nil {
		return handleError(c, h.executionPlansUnavailableError())
	}

	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return handleError(c, core.NewInvalidRequestError("execution plan id is required", nil))
	}

	view, err := h.plans.GetView(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, executionplans.ErrNotFound) {
			return handleError(c, core.NewNotFoundError("workflow not found: "+id))
		}
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, view)
}

// ListExecutionPlanGuardrails handles GET /admin/api/v1/execution-plans/guardrails
func (h *Handler) ListExecutionPlanGuardrails(c *echo.Context) error {
	if h.guardrails == nil {
		return c.JSON(http.StatusOK, []string{})
	}

	return c.JSON(http.StatusOK, h.guardrails.Names())
}

// CreateExecutionPlan handles POST /admin/api/v1/execution-plans
func (h *Handler) CreateExecutionPlan(c *echo.Context) error {
	if h.plans == nil {
		return handleError(c, h.executionPlansUnavailableError())
	}

	var req createExecutionPlanRequest
	if err := c.Bind(&req); err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}

	scopeUserPath, err := normalizeUserPathQueryParam("scope_user_path", req.ScopeUserPath)
	if err != nil {
		return handleError(c, err)
	}

	if err := h.validateExecutionPlanScope(req.ScopeProvider, req.ScopeModel); err != nil {
		return handleError(c, err)
	}

	if err := h.validateExecutionPlanGuardrails(req.Payload); err != nil {
		return handleError(c, err)
	}

	version, err := h.plans.Create(c.Request().Context(), executionplans.CreateInput{
		Scope: executionplans.Scope{
			Provider: req.ScopeProvider,
			Model:    req.ScopeModel,
			UserPath: scopeUserPath,
		},
		Activate:    true,
		Name:        req.Name,
		Description: req.Description,
		Payload:     req.Payload,
	})
	if err != nil {
		return handleError(c, executionPlanWriteError(err))
	}
	if version == nil {
		return c.NoContent(http.StatusNoContent)
	}
	return c.JSON(http.StatusCreated, version)
}

// DeactivateExecutionPlan handles POST /admin/api/v1/execution-plans/:id/deactivate
func (h *Handler) DeactivateExecutionPlan(c *echo.Context) error {
	var unavailableErr error
	var deactivate func(context.Context, string) error
	if h.plans == nil {
		unavailableErr = h.executionPlansUnavailableError()
	} else {
		deactivate = h.plans.Deactivate
	}
	return deactivateByID(c, unavailableErr, "execution plan", executionplans.ErrNotFound, "workflow not found: ", deactivate, executionPlanWriteError)
}

func (h *Handler) validateExecutionPlanGuardrails(payload executionplans.Payload) error {
	if !payload.Features.Guardrails || len(payload.Guardrails) == 0 {
		return nil
	}
	if h.guardrails == nil {
		return featureUnavailableError("guardrail registry is unavailable for plan authoring")
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

func (h *Handler) validateExecutionPlanScope(scopeProvider, scopeModel string) error {
	scopeProvider = strings.TrimSpace(scopeProvider)
	scopeModel = strings.TrimSpace(scopeModel)

	if scopeProvider == "" {
		if scopeModel != "" {
			return core.NewInvalidRequestError("scope_model requires scope_provider", nil)
		}
		return nil
	}
	if h.registry == nil {
		return core.NewInvalidRequestError("provider registry is unavailable for workflow scope validation", nil)
	}
	if !slices.Contains(h.registry.ProviderTypes(), scopeProvider) {
		return core.NewInvalidRequestError("unknown provider type: "+scopeProvider, nil)
	}
	if scopeModel == "" {
		return nil
	}

	for _, model := range h.registry.ListModelsWithProvider() {
		if model.ProviderType == scopeProvider && model.Model.ID == scopeModel {
			return nil
		}
	}
	return core.NewInvalidRequestError("unknown model for provider "+scopeProvider+": "+scopeModel, nil)
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
